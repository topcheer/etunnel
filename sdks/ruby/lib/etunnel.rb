# frozen_string_literal: true

# etunnel SDK for Ruby (requires gem install faye-websocket).
# Buffer-based protocol client (bodies buffered per stream; no WS upgrades).
#
#   gem install faye-websocket
#   ruby etunnel.rb 3000
#
# Embed:
#   require_relative 'etunnel'
#   t = Etunnel.connect('wss://t.etun.uk/tunnel', 3000)
#   puts t.url
require 'json'
require 'socket'
require 'net/http'
begin
  require 'eventmachine'
  require 'faye/websocket'
rescue LoadError => e
  # Library code must not kill the host process; surface the failure instead.
  raise LoadError, "etunnel: gem install faye-websocket (#{e.message})"
end

# faye-websocket needs a running EventMachine reactor.
unless EventMachine.reactor_running?
  Thread.new { EventMachine.run }
  sleep 0.05 until EventMachine.reactor_running?
end

MAXP = 32_768
HOP = %w[connection transfer-encoding keep-alive upgrade proxy-connection te trailer content-length host].freeze

module Etunnel
  def self.frame(kind, sid, payload)
    [kind].pack('C') + [sid].pack('N') + [payload.bytesize].pack('N') + payload
  end

  def self.connect(server, local, token: nil, subdomain: nil, resume: nil)
    local = "127.0.0.1:#{local}" unless local.to_s.include?(':')
    ws = Faye::WebSocket::Client.new(server, ['etunnel-v1'])

    queue = Queue.new
    ws.on :message do |event|
      data = event.data
      if data.is_a?(String) && data.encoding == Encoding::UTF_8 && data.start_with?('{')
        queue << [:text, data]
      else
        # binary frames arrive as ASCII-8BIT Strings (or Arrays)
        bin = data.is_a?(Array) ? data.pack('C*') : data
        queue << [:bin, bin]
      end
    end
    ws.on :close do
      queue << [:close, nil]
    end

    hello = { type: 'hello', proto: 1, client: 'etunnel-ruby/1.0' }
    hello[:token] = token if token
    hello[:subdomain] = subdomain if subdomain
    hello[:resume] = resume if resume && !resume.empty?
    ws.send(JSON.generate(hello))

    url = nil
    loop do
      kind, payload = queue.pop
      raise 'etunnel: connection closed' if kind == :close

      next unless kind == :text

      m = JSON.parse(payload)
      if m['type'] == 'welcome'
        url = m['url']
        resume_token = m['resumeToken'].to_s
        break
      elsif m['type'] == 'error'
        raise "etunnel: #{m['code']}: #{m['message']}"
      end
    end

    streams = {}
    closed = false

    Thread.new do
      loop do
        kind, payload = queue.pop
        break if kind == :close

        if kind == :text
          m = JSON.parse(payload)
          case m['type']
          when 'req'
            streams[m['streamId']] = { req: m, body: +''.b }
          when 'ping'
            ws.send(JSON.generate({ type: 'pong', ts: m['ts'] || 0 }))
          when 'bye'
            closed = true
            ws.close
            break
          end
        else
          next if payload.bytesize < 9

          kind_b, sid, n = payload.unpack('C N N')
          data = payload.byteslice(9, n)
          st = streams[sid]
          next unless st

          case kind_b
          when 0x01 then st[:body] << data
          when 0x02
            streams.delete(sid)
            # One worker thread per request (same shape as the perl SDK): a slow
            # local app must not block this reader loop, or server pings go
            # unanswered while a request is in flight and the tunnel gets kicked.
            Thread.new do
              serve(ws, st[:req], st[:body], sid, local)
            rescue StandardError => e
              warn "etunnel: request worker failed: #{e}"
            end
          when 0x03 then streams.delete(sid)
          end
        end
      end
    end

    tunnel = Object.new
    tunnel.define_singleton_method(:url) { url }
    tunnel.define_singleton_method(:resume_token) { resume_token }
    tunnel.define_singleton_method(:close) do
      ws.send(JSON.generate({ type: 'bye', reason: 'client closed' }))
      ws.close
    end
    tunnel
  end

  # Themed 502 page shown when the local app is down (same HTML as the other SDKs).
  def self.local_error_page(error)
    esc = error.to_s.gsub('&', '&amp;').gsub('<', '&lt;').gsub('>', '&gt;')
    <<~HTML
      <!doctype html><html><head><meta charset="utf-8">
      <meta name="viewport" content="width=device-width, initial-scale=1">
      <title>502 - local app down</title>
      <style>
      :root{--bg:#0b0e14;--panel:#0d1117;--line:#21262d;--fg:#e6edf3;--dim:#8b949e;--green:#3fb950}
      *{box-sizing:border-box;margin:0;padding:0}
      body{font-family:ui-monospace,'SF Mono',Menlo,Consolas,monospace;background:var(--bg);color:var(--fg);
      display:flex;min-height:100vh;align-items:center;justify-content:center;line-height:1.6}
      main{max-width:600px;padding:2.5rem 1.5rem;width:100%}
      .badge{display:inline-block;border:1px solid rgba(248,81,73,.4);color:#f85149;border-radius:99px;
      padding:.2rem .8rem;font-size:.72rem;letter-spacing:.08em;text-transform:uppercase;margin-bottom:1.1rem}
      h1{font-size:1.6rem;letter-spacing:-.01em;margin-bottom:.4rem}
      h1 .g{color:var(--green)}
      p{color:var(--dim);font-size:.9rem;margin-bottom:1.4rem}
      .panel{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:1.1rem 1.3rem;margin-bottom:1.1rem}
      .panel .k{color:var(--dim);font-size:.72rem;text-transform:uppercase;letter-spacing:.08em;margin-bottom:.3rem}
      .panel .v{font-size:.9rem;word-break:break-all}
      .err{color:#f85149}
      .tip{border-left:3px solid var(--green);padding:.8rem 1rem;margin-top:1.2rem;font-size:.82rem;color:var(--dim)}
      .tip b{color:var(--fg)}
      footer{margin-top:2rem;font-size:.75rem;color:var(--dim)}
      footer a{color:#58a6ff;text-decoration:none}
      .pulse{display:inline-block;width:.55em;height:.55em;border-radius:50%;background:var(--green);
      margin-right:.5em;animation:p 1.2s ease-in-out infinite}
      @keyframes p{0%,100%{opacity:1}50%{opacity:.25}}
      @media(prefers-reduced-motion:reduce){.pulse{animation:none}}
      </style></head><body><main>
      <span class="badge">502 local app down</span>
      <h1>tunnel up, <span class="g">local app</span> is not</h1>
      <p>The tunnel connection is alive, but nothing answered when we knocked.</p>
      <div class="panel">
      <div class="k">error</div><div class="v err">#{esc}</div>
      </div>
      <div class="tip"><span class="pulse"></span><b>Fix:</b> start your local service and reload -
      the tunnel stays connected and keeps retrying for you.</div>
      <footer>powered by <a href="https://github.com/topcheer/etunnel">etunnel</a></footer>
      </main></body></html>
    HTML
  end

  def self.serve(ws, req, body, sid, local)
    host, port = local.split(':')
    method = req['method'] || 'GET'
    path = req['path'] || '/'
    headers = (req['headers'] || {}).reject { |k, _| HOP.include?(k.downcase) }

    # Runs on its own worker thread (see the reader loop); no inner
    # Thread.new{}.value needed - that would just re-block the caller.
    status, headers, payload = begin
      http = Net::HTTP.new(host, port.to_i)
      http.open_timeout = 10
      klass = Net::HTTP.const_get(method.capitalize)
      r = klass.new(path, headers.to_h { |k, v| [k, Array(v).first] })
      r.body = body unless method == 'GET' || method == 'HEAD'
      resp = http.request(r)
      [resp.code.to_i, resp.each_header.reject { |k, _| HOP.include?(k) }.to_h, resp.body || '']
    rescue StandardError => e
      [502, { 'Content-Type' => 'text/html; charset=utf-8' }, local_error_page(e)]
    end

    ws.send(JSON.generate({ type: 'res', streamId: sid, status: status,
                            headers: headers.to_h { |k, v| [k, [v]] } }))
    payload = payload.b
    i = 0
    while i < payload.bytesize
      ws.send(frame(0x01, sid, payload.byteslice(i, MAXP)))
      i += MAXP
    end
    ws.send(frame(0x02, sid, ''))
  end
end

def Etunnel.main(argv = ARGV)
  server = ENV['ETUNNEL_SERVER'] || 'wss://t.etun.uk/tunnel'
  local = argv[0] || ENV['ETUNNEL_LOCAL'] || '3000'
  token = ENV['ETUNNEL_TOKEN']
  subdomain = ENV['ETUNNEL_SUBDOMAIN']
  reconnect = ENV['ETUNNEL_RECONNECT'] != '0'

  $stdout.sync = true
  stopped = false
  trap(:INT) { stopped = true }
  trap(:TERM) { stopped = true }

  # 500ms doubling, capped at 30s, +-20% jitter.
  backoff = ->(attempt) { [0.5 * (2**attempt), 30.0].min * (0.8 + (rand * 0.4)) }
  nap = lambda do |seconds|
    deadline = Process.clock_gettime(Process::CLOCK_MONOTONIC) + seconds
    sleep 0.25 while !stopped && Process.clock_gettime(Process::CLOCK_MONOTONIC) < deadline
  end
  shutdown = lambda do |tunnel|
    tunnel.close
  rescue StandardError
    nil
  end

  attempt = 0
  resume = ''
  until stopped
    # The SDK exposes no close callback, but the reader thread it starts dies
    # with the websocket: watch it for the disconnect signal.
    before = Thread.list
    begin
      tunnel = Etunnel.connect(server, local, token: token, subdomain: subdomain, resume: resume)
      resume = tunnel.resume_token if tunnel.respond_to?(:resume_token) && !tunnel.resume_token.empty?
    rescue StandardError => e
      warn(e.message.start_with?('etunnel:') ? e.message : "etunnel: #{e}")
      exit 1 unless reconnect

      delay = backoff.call(attempt)
      attempt += 1
      warn format('etunnel: reconnecting in %.1fs', delay)
      nap.call(delay)
      next
    end

    workers = Thread.list - before
    puts "TUNNEL_URL: #{tunnel.url}"
    attempt = 0

    unless reconnect
      sleep 0.25 until stopped
      shutdown.call(tunnel)
      break
    end

    sleep 0.25 while !stopped && workers.any?(&:alive?)
    if stopped
      shutdown.call(tunnel)
      break
    end

    delay = backoff.call(attempt)
    attempt += 1
    warn 'etunnel: connection lost'
    warn format('etunnel: reconnecting in %.1fs', delay)
    nap.call(delay)
  end
end
Etunnel.main if $PROGRAM_NAME == __FILE__
