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
  # SDK version reported in hello.version (stamped by scripts/bump-release.py).
  VERSION = '1.7.44'.freeze

  def self.frame(kind, sid, payload)
    [kind].pack('C') + [sid].pack('N') + [payload.bytesize].pack('N') + payload
  end

  # --- multi-target local routing (PROTOCOL.md §2) ---------------------------
  Route = Struct.new(:prefix, :addr, :strip)

  # Validate + compile a route table: trims trailing slashes ("/" stays),
  # rejects duplicates and malformed prefixes, sorts longest-first.
  def self.normalize_routes(routes)
    out = []
    seen = {}
    routes.each_with_index do |r, i|
      pfx = r.prefix.to_s
      raise ArgumentError, "etunnel: route[#{i}]: prefix is required (use \"/\" for the fallback route)" if pfx.empty?
      raise ArgumentError, "etunnel: route[#{i}]: addr is required" if r.addr.to_s.empty?
      raise ArgumentError, "etunnel: route[#{i}]: prefix \"#{pfx}\" must start with '/'" unless pfx.start_with?('/')
      pfx = pfx[0..-2] while pfx.length > 1 && pfx.end_with?('/')
      raise ArgumentError, "etunnel: route[#{i}]: duplicate prefix \"#{pfx}\"" if seen.key?(pfx)
      seen[pfx] = true
      out << Route.new(pfx, r.addr, r.strip ? true : false)
    end
    out.sort_by { |r| -r.prefix.length }
  end

  # Resolve "path?query": [addr, path] on hit, nil when nothing matches.
  def self.route_for(routes, path_and_query)
    qi = path_and_query.index('?')
    path = qi ? path_and_query[0...qi] : path_and_query
    query = qi ? path_and_query[qi..] : ''
    routes.each do |r|
      next unless r.prefix == '/' || path == r.prefix || path.start_with?("#{r.prefix}/")

      p = path
      p = (path == r.prefix ? '/' : path[r.prefix.length..] || '') if r.strip && r.prefix != '/'
      return [r.addr, p + query]
    end
    nil
  end

  # Handle returned by Etunnel.connect (#417-E27): url / resume_token
  # readers plus close (sends bye, then closes the socket).
  class Tunnel
    attr_reader :url, :resume_token

    def initialize(url, resume_token, socket)
      @url = url
      @resume_token = resume_token
      @socket = socket
    end

    def close
      begin
        @socket.send(JSON.generate({ type: 'bye', reason: 'client closed' }))
      rescue StandardError
        nil # socket already gone - still close below
      end
      begin
        @socket.close
      rescue StandardError
        nil
      end
      nil
    end
  end

  def self.connect(server, local, token: nil, subdomain: nil, resume: nil, routes: nil)
    routes = normalize_routes(routes) if routes
    raise ArgumentError, 'etunnel: routes must not be empty' if routes && routes.empty?
    local = "127.0.0.1:#{local}" unless local.nil? || local.to_s.include?(':')
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

    hello = { type: 'hello', proto: 1, client: "etunnel-ruby/#{VERSION}", version: VERSION }
    hello[:token] = token if token
    hello[:subdomain] = subdomain if subdomain
    hello[:resume] = resume if resume && !resume.empty?
    ws.send(JSON.generate(hello))

    url = nil
    resume_token = '' # #417-E27: was only set inside the resumeToken branch - uninitialized otherwise
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
              serve(ws, st[:req], st[:body], sid, local, routes)
            rescue StandardError => e
              warn "etunnel: request worker failed: #{e}"
            end
          when 0x03 then streams.delete(sid)
          end
        end
      end
    end

    # #417-E27 (P0, Ruby 4.0): the tunnel used to be an Object.new with three
    # define_singleton_method blocks closing over locals. On Ruby 4.0 a
    # block handed to define_singleton_method runs with the receiver as
    # self and bare locals inside it (url, ws) resolve as method calls on
    # that receiver - so `url` re-entered the getter we were defining:
    # `stack level too deep` at the first tunnel.url read, same hazard for
    # close's `ws`. Replaced with a plain class whose constructor captures
    # the values: deterministic closure semantics on every Ruby version.
    Tunnel.new(url, resume_token, ws)
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

  def self.serve(ws, req, body, sid, local, routes = nil)
    if routes
      hit = route_for(routes, req['path'] || '/')
      unless hit
        ws.send(JSON.generate({ type: 'res', streamId: sid, status: 404,
                                headers: { 'Content-Type' => ['text/plain; charset=utf-8'] } }))
        ws.send(frame(0x01, sid, "404 page not found (no route matches this path)\n"))
        ws.send(frame(0x02, sid, ''))
        return
      end
      addr, = hit
      local = if addr =~ /\A\d+\z/
                "127.0.0.1:#{addr}"
              elsif (m = addr.match(/\A(\[[^\]]+\]|[^:]+):(\d+)\z/))
                "#{m[1]}:#{m[2]}"
              else
                "#{addr}:80"
              end
    end
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

# --- CLI loopback login (RFC 8252 style; server /auth/cli/* endpoints) ---
  #   creds = Etunnel.login('https://t.etun.uk')  # opens the browser
  #   # -> { 'api_token' => ..., 'subdomains' => [...], 'plan' => ..., 'expires_at' => 0 }
  # Binds a random 127.0.0.1 port, waits for the one-time code on /callback
  # (10-minute budget), exchanges it. Persist the result and pass api_token
  # as token: on future Etunnel.connect calls.
def Etunnel.login(server, open_browser: nil, timeout: 600)
    require 'socket'
    require 'net/http'
    require 'json'
    require 'uri'
    base = server.to_s.sub(%r{/+\z}, '')
    raise ArgumentError, 'etunnel: login: server required' if base.empty?

    server_sock = TCPServer.new('127.0.0.1', 0)
    port = server_sock.addr[1]

    th = Thread.new do
      # Accept exactly one request: the browser's redirect to /callback?code=...
      client = server_sock.accept
      req = client.recv(4096)
      path = req.split(' ')[1].to_s
      code = path[/[?&]code=([^&\s]+)/, 1].to_s
      body = '<!doctype html><p>Authorized. You can close this tab and return to the terminal.</p>'
      client.write("HTTP/1.0 200 OK\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: #{body.bytesize}\r\nConnection: close\r\n\r\n#{body}")
      client.close
      code
    end

    uri = URI(base + '/auth/cli/start')
    start = JSON.parse(Net::HTTP.post(uri, { redirect_uri: "http://127.0.0.1:#{port}/callback" }.to_json, 'Content-Type' => 'application/json').body)
    raise 'etunnel: login: empty start payload' if start['auth_url'].to_s.empty? || start['device_code'].to_s.empty?

    opened = false
    begin
      opener = open_browser || ->(u) { system(RUBY_PLATFORM =~ /darwin/ ? 'open' : 'xdg-open', u) }
      opened = opener.call(start['auth_url'])
    rescue StandardError
      opened = false
    end
    $stderr.puts "etunnel: authorize in your browser: #{start['auth_url']}" unless opened

    th.join(timeout)
    raise 'etunnel: login timed out' if th.alive?
    code = th.value
    server_sock.close
    raise 'etunnel: login: callback without code' if code.empty?

    ex = URI(base + '/auth/cli/exchange')
    creds = JSON.parse(Net::HTTP.post(ex, { device_code: start['device_code'], code: code }.to_json, 'Content-Type' => 'application/json').body)
    raise 'etunnel: login: no api_token in exchange response' if creds['api_token'].to_s.empty?
    creds
  end

Etunnel.main if $PROGRAM_NAME == __FILE__

