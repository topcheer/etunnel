# frozen_string_literal: true

# etunnel SDK for Ruby (requires gem install faye-websocket).
# Buffer-based protocol client (bodies buffered per stream; no WS upgrades).
#
#   gem install faye-websocket
#   ruby etunnel.rb 3000
#
# Embed:
#   require_relative 'etunnel'
#   t = Etunnel.connect('wss://t.ggcode.uk/tunnel', 3000)
#   puts t.url
require 'json'
require 'socket'
require 'net/http'
begin
  require 'eventmachine'
  require 'faye/websocket'
rescue LoadError
  warn 'etunnel: gem install faye-websocket'
  exit 1
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

  def self.connect(server, local, token: nil, subdomain: nil)
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
    ws.send(JSON.generate(hello))

    url = nil
    loop do
      kind, payload = queue.pop
      raise 'etunnel: connection closed' if kind == :close

      next unless kind == :text

      m = JSON.parse(payload)
      if m['type'] == 'welcome'
        url = m['url']
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
            serve(ws, st[:req], st[:body], sid, local)
          when 0x03 then streams.delete(sid)
          end
        end
      end
    end

    tunnel = Object.new
    tunnel.define_singleton_method(:url) { url }
    tunnel.define_singleton_method(:close) do
      ws.send(JSON.generate({ type: 'bye', reason: 'client closed' }))
      ws.close
    end
    tunnel
  end

  def self.serve(ws, req, body, sid, local)
    host, port = local.split(':')
    method = req['method'] || 'GET'
    path = req['path'] || '/'
    headers = (req['headers'] || {}).reject { |k, _| HOP.include?(k.downcase) }

    out = Thread.new do
      begin
        http = Net::HTTP.new(host, port.to_i)
        http.open_timeout = 10
        klass = Net::HTTP.const_get(method.capitalize)
        r = klass.new(path, headers.to_h { |k, v| [k, Array(v).first] })
        r.body = body unless method == 'GET' || method == 'HEAD'
        resp = http.request(r)
        [resp.code.to_i, resp.each_header.reject { |k, _| HOP.include?(k) }.to_h, resp.body || '']
      rescue StandardError => e
        [502, { 'Content-Type' => 'text/plain' }, "etunnel: local service error: #{e}\n"]
      end
    end.value

    status, headers, payload = out
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
  server = ENV['ETUNNEL_SERVER'] || 'wss://t.ggcode.uk/tunnel'
  local = argv[0] || ENV['ETUNNEL_LOCAL'] || '3000'
  begin
    t = Etunnel.connect(server, local,
                        token: ENV['ETUNNEL_TOKEN'], subdomain: ENV['ETUNNEL_SUBDOMAIN'])
  rescue StandardError => e
    warn "etunnel: #{e}"
    exit 1
  end
  $stdout.sync = true
  puts "TUNNEL_URL: #{t.url}"
  trap(:INT) { t.close; exit 0 }
  sleep 1 while true
end
Etunnel.main if $PROGRAM_NAME == __FILE__
