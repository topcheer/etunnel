// etunnel SDK for Swift (Foundation only, run as a script).
// Buffer-based protocol client (bodies buffered per stream; no WS upgrades).
//
// Run: swift etunnel.swift 3000
import Foundation

let MAXP = 32768

func frame(_ kind: UInt8, _ id: UInt32, _ payload: [UInt8]) -> [UInt8] {
    var b = [UInt8](repeating: 0, count: 9 + payload.count)
    b[0] = kind
    b[1] = UInt8((id >> 24) & 0xFF); b[2] = UInt8((id >> 16) & 0xFF)
    b[3] = UInt8((id >> 8) & 0xFF); b[4] = UInt8(id & 0xFF)
    let n = UInt32(payload.count)
    b[5] = UInt8((n >> 24) & 0xFF); b[6] = UInt8((n >> 16) & 0xFF)
    b[7] = UInt8((n >> 8) & 0xFF); b[8] = UInt8(n & 0xFF)
    for (i, x) in payload.enumerated() { b[9 + i] = x }
    return b
}

let hopHeaders: Set<String> = ["connection", "transfer-encoding", "keep-alive",
    "upgrade", "content-length", "host", "proxy-connection"]

actor StreamStore {
    var streams: [UInt32: (req: [String: Any], body: [[UInt8]])] = [:]
    func put(_ sid: UInt32, _ req: [String: Any]) { streams[sid] = (req, []) }
    func append(_ sid: UInt32, _ chunk: [UInt8]) -> Bool {
        guard streams[sid] != nil else { return false }
        streams[sid]!.body.append(chunk)
        return true
    }
    func take(_ sid: UInt32) -> (req: [String: Any], body: [[UInt8]])? {
        defer { streams[sid] = nil }
        return streams[sid]
    }
    func drop(_ sid: UInt32) { streams[sid] = nil }
}

final class Etunnel: @unchecked Sendable {
    let task: URLSessionWebSocketTask
    let localHost: String
    let localPort: Int
    let session: URLSession
    let store = StreamStore()
  private let stateLock = NSLock()
  private var closedFlag = false
  /// Server-signed resume token from the welcome message. Feeding it back on
  /// reconnect keeps the same subdomain instead of a fresh random one.
  private(set) var resumeToken: String = ""

  /// True once the receive loop is over (socket error, close or bye).
  /// The CLI supervisor polls it to decide when to reconnect.
  var isClosed: Bool {
    stateLock.lock()
    defer { stateLock.unlock() }
    return closedFlag
  }

  func markClosed() {
    stateLock.lock()
    closedFlag = true
    stateLock.unlock()
  }

    init?(server: String, local: String) {
        if let i = local.lastIndex(of: ":"), let p = Int(local[local.index(after: i)...]) {
            self.localHost = String(local[..<i])
            self.localPort = p
        } else {
            self.localHost = "127.0.0.1"
            self.localPort = Int(local) ?? 3000
        }
        // A malformed ETUNNEL_SERVER used to crash here on URL(string:)! ;
        // the caller now gets nil and reports it instead of a process abort (#53).
        guard let u = URL(string: server), u.scheme != nil, u.host != nil else { return nil }
        let cfg = URLSessionConfiguration.ephemeral
        cfg.timeoutIntervalForRequest = 15
        self.session = URLSession(configuration: cfg)
        self.task = session.webSocketTask(with: u, protocols: ["etunnel-v1"])
    }

    func connect(token: String? = nil, subdomain: String? = nil, resume: String? = nil) async throws -> String {
        task.resume()
        var hello: [String: Any] = ["type": "hello", "proto": 1, "client": "etunnel-swift/1.0"]
        if let t = token { hello["token"] = t }
        if let s = subdomain { hello["subdomain"] = s }
        if let r = resume, !r.isEmpty { hello["resume"] = r }
        let data = try JSONSerialization.data(withJSONObject: hello)
        try await task.send(.string(String(data: data, encoding: .utf8)!))
        for _ in 0..<30 {
            let msg = try await task.receive()
            if case .string(let text) = msg,
               let d = text.data(using: .utf8),
               let obj = try? JSONSerialization.jsonObject(with: d) as? [String: Any],
               let type = obj["type"] as? String {
                if type == "welcome" {
                    if let rt = obj["resumeToken"] as? String { resumeToken = rt }
                    // server-controlled field: as! here was a protocol-deviation
                    // crash (#53)
                    guard let u = obj["url"] as? String else {
                        throw NSError(domain: "etunnel", code: 3,
                            userInfo: [NSLocalizedDescriptionKey: "welcome missing url"])
                    }
                    startLoop()
                    return u
                }
                if type == "error" {
                    throw NSError(domain: "etunnel", code: 1,
                        userInfo: [NSLocalizedDescriptionKey: "\(obj["code"] ?? ""): \(obj["message"] ?? "")"])
                }
            }
        }
        throw NSError(domain: "etunnel", code: 2, userInfo: [NSLocalizedDescriptionKey: "welcome timeout"])
    }

    func startLoop() {
        Task.detached { await self.loop() }
    }

  func loop() async {
    defer { markClosed() }
    while true {
            guard let msg = try? await task.receive() else { return }
            switch msg {
            case .string(let text):
                guard let d = text.data(using: .utf8),
                      let obj = (try? JSONSerialization.jsonObject(with: d)) as? [String: Any],
                      let type = obj["type"] as? String else { continue }
                switch type {
                case "req":
                    // a non-numeric streamId in the frame used to crash the
                    // process (#53)
                    guard let sidVal = obj["streamId"] as? Int else { continue }
                    let sid = UInt32(sidVal)
                    await store.put(sid, obj)
                case "ping":
                    let ts = obj["ts"] ?? 0
                    if let d2 = try? JSONSerialization.data(withJSONObject: ["type": "pong", "ts": ts]),
                       let s = String(data: d2, encoding: .utf8) {
                        try? await task.send(.string(s))
                    }
                case "bye":
                    return
                default: break
                }
            case .data(let data):
                let b = [UInt8](data)
                guard b.count >= 9 else { continue }
                let sid = UInt32(b[1]) << 24 | UInt32(b[2]) << 16 | UInt32(b[3]) << 8 | UInt32(b[4])
                let n = Int(UInt32(b[5]) << 24 | UInt32(b[6]) << 16 | UInt32(b[7]) << 8 | UInt32(b[8]))
                let payload = Array(b[9..<min(9 + n, b.count)])
                switch b[0] {
                case 0x01:
                    _ = await store.append(sid, payload)
                case 0x02:
                    if let st = await store.take(sid) {
                        let body = st.body.flatMap { $0 }
                        Task.detached { await self.serve(st.req, body: body, sid: sid) }
                    }
                case 0x03:
                    await store.drop(sid)
                default: break
                }
            @unknown default: break
            }
        }
    }

    func serve(_ req: [String: Any], body: [UInt8], sid: UInt32) async {
        let method = req["method"] as? String ?? "GET"
        let path = req["path"] as? String ?? "/"
        guard let url = URL(string: "http://\(localHost):\(localPort)\(path)") else { return }
        var urlReq = URLRequest(url: url)
        urlReq.httpMethod = method
        urlReq.httpBody = (method == "GET" || method == "HEAD") ? nil : Data(body)
        if let hdrs = req["headers"] as? [String: [String]] {
            for (k, vs) in hdrs {
                if hopHeaders.contains(k.lowercased()) { continue }
                for v in vs { urlReq.addValue(v, forHTTPHeaderField: k) }
            }
        }
        do {
            let (data, resp) = try await session.data(for: urlReq)
            // non-HTTP answers (captive portal, proxy error page) used to
            // crash the cast (#53)
            guard let http = resp as? HTTPURLResponse else { return }
            var hdrs: [String: [String]] = [:]
            for (k, v) in http.allHeaderFields {
                if let ks = k as? String, let vs = v as? String {
                    if hopHeaders.contains(ks.lowercased()) { continue }
                    hdrs[ks, default: []].append(vs)
                }
            }
            let res: [String: Any] = ["type": "res", "streamId": Int(sid), "status": http.statusCode, "headers": hdrs]
            if let d = try? JSONSerialization.data(withJSONObject: res),
               let s = String(data: d, encoding: .utf8) {
                try? await task.send(.string(s))
            }
            var i = 0
            let bytes = [UInt8](data)
            while i < bytes.count {
                let n = min(MAXP, bytes.count - i)
                try? await task.send(.data(Data(frame(0x01, sid, Array(bytes[i..<(i + n)])))))
                i += n
            }
            try? await task.send(.data(Data(frame(0x02, sid, []))))
        } catch {
            let res: [String: Any] = ["type": "res", "streamId": Int(sid), "status": 502,
                "headers": ["Content-Type": ["text/html; charset=utf-8"]]]
            if let d = try? JSONSerialization.data(withJSONObject: res),
               let s = String(data: d, encoding: .utf8) {
                try? await task.send(.string(s))
            }
            let msg = localErrorPage(error.localizedDescription)
            try? await task.send(.data(Data(frame(0x01, sid, Array(msg.utf8)))))
            try? await task.send(.data(Data(frame(0x02, sid, []))))
        }
    }

    // Themed 502 page shown when the local app is down (same HTML as the other SDKs).
    func localErrorPage(_ msg: String) -> String {
        let esc = msg.replacingOccurrences(of: "&", with: "&amp;")
            .replacingOccurrences(of: "<", with: "&lt;")
            .replacingOccurrences(of: ">", with: "&gt;")
        return """
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
        <div class="k">error</div><div class="v err">\(esc)</div>
        </div>
        <div class="tip"><span class="pulse"></span><b>Fix:</b> start your local service and reload -
        the tunnel stays connected and keeps retrying for you.</div>
        <footer>powered by <a href="https://github.com/topcheer/etunnel">etunnel</a></footer>
        </main></body></html>
        """
    }

    func close() {
        Task { try? await task.send(.string("{\"type\":\"bye\",\"reason\":\"client closed\"}")) }
        task.cancel(with: .normalClosure, reason: nil)
        // a fresh URLSession per connection was never invalidated: a long
        // reconnect loop accumulated them (#53)
        session.invalidateAndCancel()
    }
}

// --- CLI loopback login (RFC 8252 style; server /auth/cli/* endpoints) ---
//   let creds = try etunnelLogin("https://t.etun.uk")   // opens the browser
//   // -> LoginCredentials(apiToken, subdomains, plan). Persist it and pass
//   //    apiToken as the token option on future Etunnel.connect calls.
public struct LoginCredentials {
    public let apiToken: String
    public let subdomains: [String]
    public let plan: String
}

public func etunnelLogin(_ server: String, openBrowser: ((String) -> Void)? = nil,
                         timeout: TimeInterval = 600,
                         queue: DispatchQueue = .global()) throws -> LoginCredentials {
    let base = server.replacingOccurrences(of: "/", with: "", options: .backwards, range: server.range(of: "/+$", options: .regularExpression) ?? server.endIndex..<server.endIndex)
    let baseClean = String(server.reversed().drop(while: { $0 == "/" }).reversed())
    precondition(!baseClean.isEmpty, "etunnel: login: server required")

    // 1) claim an ephemeral loopback port with a POSIX socket
    let fd = socket(AF_INET, SOCK_STREAM, 0)
    guard fd >= 0 else { throw NSError(domain: "etunnel", code: 1, userInfo: [NSLocalizedDescriptionKey: "etunnel: login: socket() failed"]) }
    defer { close(fd) }
    var addr = sockaddr_in()
    addr.sin_family = sa_family_t(AF_INET)
    addr.sin_addr.s_addr = inet_addr("127.0.0.1")
    addr.sin_port = 0
    var on: Int32 = 1
    setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &on, socklen_t(MemoryLayout<Int32>.size))
    guard bind(fd, sockaddr_cast(&addr), socklen_t(MemoryLayout<sockaddr_in>.size)) == 0,
          listen(fd, 1) == 0 else {
        throw NSError(domain: "etunnel", code: 2, userInfo: [NSLocalizedDescriptionKey: "etunnel: login: bind/listen failed"])
    }
    var bound = sockaddr_in()
    var len = socklen_t(MemoryLayout<sockaddr_in>.size)
    getsockname(fd, sockaddr_cast(&bound), &len)
    let port = Int(bound.sin_port.bigEndian)

    // 2) start the device flow
    let sem = DispatchSemaphore(value: 0)
    var startPayload: [String: Any] = [:]
    var startErr: Error?
    postJson("\(baseClean)/auth/cli/start", ["redirect_uri": "http://127.0.0.1:\(port)/callback"]) { r in
        switch r {
        case .success(let d): startPayload = d
        case .failure(let e): startErr = e
        }
        sem.signal()
    }
    sem.wait()
    if let e = startErr { throw e }
    guard let authUrl = startPayload["auth_url"] as? String,
          let deviceCode = startPayload["device_code"] as? String else {
        throw NSError(domain: "etunnel", code: 3, userInfo: [NSLocalizedDescriptionKey: "etunnel: login: empty start payload"])
    }

    // 3) open the browser (URL printed when headless)
    var opened = false
    if let opener = openBrowser {
        opener(authUrl); opened = true
    } else if let u = URL(string: authUrl) {
        opened = NSWorkspace.shared.open(u) // macOS; on Linux the task below is the fallback
    }
    if !opened {
        #if os(macOS)
        #else
        _ = try? Process.run(URL(fileURLWithPath: "/usr/bin/xdg-open"), arguments: [authUrl])
        #endif
        FileHandle.standardError.write("etunnel: authorize in your browser: \(authUrl)\n".data(using: .utf8)!)
    }

    // 4) block on the single callback (poll the socket with a deadline)
    let deadline = Date().addingTimeInterval(timeout)
    var code = ""
    while code.isEmpty {
        if Date() > deadline {
            throw NSError(domain: "etunnel", code: 4, userInfo: [NSLocalizedDescriptionKey: "etunnel: login timed out"])
        }
        var fds = pollfd(fd: fd, events: Int16(POLLIN), revents: 0)
        _ = withUnsafePointer(to: &fds) { ptr in
            ptr.withMemoryRebound(to: pollfd.self, capacity: 1) { poll($0, 1, 1000) }
        }
        var client = sockaddr_in(); var clen = socklen_t(MemoryLayout<sockaddr_in>.size)
        let cfd = accept(fd, sockaddr_cast(&client), &clen)
        guard cfd >= 0 else { continue }
        defer { close(cfd) }
        var buf = [UInt8](repeating: 0, count: 4096)
        let n = recv(cfd, &buf, buf.count, 0)
        let req = String(bytes: buf[0..<max(0, n)], encoding: .utf8) ?? ""
        if let q = req.split(separator: " ").dropFirst().first,
           let kv = q.split(separator: "?").dropFirst().first {
            for pair in kv.split(separator: "&") where pair.hasPrefix("code=") {
                code = String(pair.dropFirst("code=".count)).removingPercentEncoding ?? String(pair.dropFirst("code=".count))
            }
        }
        let body = "<!doctype html><p>Authorized. You can close this tab and return to the terminal.</p>"
        let resp = "HTTP/1.0 200 OK\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: \(body.utf8.count)\r\nConnection: close\r\n\r\n\(body)"
        _ = resp.withCString { send(cfd, $0, strlen($0), 0) }
    }

    // 5) exchange
    var exPayload: [String: Any] = [:]
    var exErr: Error?
    let sem2 = DispatchSemaphore(value: 0)
    postJson("\(baseClean)/auth/cli/exchange", ["device_code": deviceCode, "code": code]) { r in
        switch r {
        case .success(let d): exPayload = d
        case .failure(let e): exErr = e
        }
        sem2.signal()
    }
    sem2.wait()
    if let e = exErr { throw e }
    guard let token = exPayload["api_token"] as? String, !token.isEmpty else {
        throw NSError(domain: "etunnel", code: 5, userInfo: [NSLocalizedDescriptionKey: "etunnel: login: no api_token in exchange response"])
    }
    let subs = (exPayload["subdomains"] as? [Any])?.map { String(describing: $0) } ?? []
    return LoginCredentials(apiToken: token, subdomains: subs, plan: exPayload["plan"] as? String ?? "")
}

private func sockaddr_cast(_ p: UnsafeMutablePointer<sockaddr_in>) -> UnsafeMutablePointer<sockaddr> {
    UnsafeMutableRawPointer(p).assumingMemoryBound(to: sockaddr.self)
}

private func postJson(_ url: String, _ body: [String: String], done: @escaping (Result<[String: Any], Error>) -> Void) {
    var req = URLRequest(url: URL(string: url)!)
    req.httpMethod = "POST"
    req.setValue("application/json", forHTTPHeaderField: "Content-Type")
    let flat = body.map { "\"\($0.key)\":\"\($0.value)\"" }.joined(separator: ",")
    req.httpBody = "{\(flat)}".data(using: .utf8)
    URLSession.shared.dataTask(with: req) { data, resp, err in
        if let err = err { return done(.failure(err)) }
        let status = (resp as? HTTPURLResponse)?.statusCode ?? 0
        guard let data = data, status == 200,
              let obj = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] else {
            let snippet = String(data: data ?? Data(), encoding: .utf8)?.prefix(200) ?? ""
            return done(.failure(NSError(domain: "etunnel", code: 6, userInfo: [NSLocalizedDescriptionKey: "etunnel: login: HTTP \(status): \(snippet)"])))
        }
        done(.success(obj))
    }.resume()
}
