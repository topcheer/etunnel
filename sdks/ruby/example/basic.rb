# Minimal etunnel example: serve a local HTTP page through a public tunnel.
#   ruby -Ilib example/basic.rb            # local port 3000
#   ruby -Ilib example/basic.rb 8080
require 'socket'
require_relative '../lib/etunnel'

port = (ARGV[0] || 3000).to_i
Thread.new do
  server = TCPServer.new('127.0.0.1', port)
  loop do
    client = server.accept
    client.gets("\r\n\r\n")
    body = "hello from etunnel-example\n"
    client.write("HTTP/1.1 200 OK\r\ncontent-type: text/plain\r\ncontent-length: #{body.bytesize}\r\n\r\n#{body}")
    client.close
  end
end
puts "local http://127.0.0.1:#{port}"

t = Etunnel.connect('wss://t.etun.uk/tunnel', port)
puts "public URL: #{t[:url]}"
sleep
