// Minimal etunnel example (Swift 5.9+).
//   swift run etunnel-example 3000   (add as an executable target; see Package.swift)
// Or embed: the code below is the whole program.
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

print("local http://127.0.0.1:\(port)")
let tunnel = try connect(server: "wss://t.etun.uk/tunnel", localPort: port)
print("public URL: \(tunnel.url)")
RunLoop.main.run()
