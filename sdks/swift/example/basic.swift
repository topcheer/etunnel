// Minimal etunnel example (Swift 5.9+) - a local server plus a tunnel in one file.
//
// The client is deliberately a single file, so the simplest way to run this
// example is to concatenate it with the client and run the result as a script:
//
//   cd sdks/swift
//   cat etunnel.swift example/basic.swift > /tmp/basic.swift
//   swift /tmp/basic.swift 3000
//
// In a real app, add etunnel.swift to your target and call Etunnel directly
// (see README.md); the CLI target `swift run etunnel 3000` exposes an existing
// local service without any code changes.
import Foundation

let port = CommandLine.arguments.count > 1 ? Int(CommandLine.arguments[1]) ?? 3000 : 3000

// Tiny echo HTTP server on a background thread.
DispatchQueue.global().async {
    let sock = socket(AF_INET, SOCK_STREAM, 0)
    var yes: Int32 = 1
    setsockopt(sock, SOL_SOCKET, SO_REUSEADDR, &yes, socklen_t(MemoryLayout<Int32>.size))
    var addr = sockaddr_in()
    addr.sin_family = sa_family_t(AF_INET)
    addr.sin_port = UInt16(port).bigEndian
    addr.sin_addr.s_addr = INADDR_ANY
    withUnsafePointer(to: &addr) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
            _ = bind(sock, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
        }
    }
    listen(sock, 16)
    while true {
        let client = accept(sock, nil, nil)
        guard client >= 0 else { continue }
        let body = "hello from etunnel-example\n"
        let resp = "HTTP/1.1 200 OK\r\ncontent-type: text/plain\r\ncontent-length: \(body.utf8.count)\r\n\r\n\(body)"
        _ = resp.withCString { write(client, $0, strlen($0)) }
        close(client)
    }
}

print("local service: http://127.0.0.1:\(port)")

let server = ProcessInfo.processInfo.environment["ETUNNEL_SERVER"] ?? "wss://t.etun.uk/tunnel"
let tunnel = Etunnel(server: server, local: String(port))
let url = try await tunnel.connect(
    token: ProcessInfo.processInfo.environment["ETUNNEL_TOKEN"],
    subdomain: ProcessInfo.processInfo.environment["ETUNNEL_SUBDOMAIN"])
print("public URL: \(url)")

RunLoop.main.run()
