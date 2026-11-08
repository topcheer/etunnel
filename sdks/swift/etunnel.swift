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

    init(server: String, local: String) {
        if let i = local.lastIndex(of: ":"), let p = Int(local[local.index(after: i)...]) {
            self.localHost = String(local[..<i])
            self.localPort = p
        } else {
            self.localHost = "127.0.0.1"
            self.localPort = Int(local) ?? 3000
        }
        let cfg = URLSessionConfiguration.ephemeral
        cfg.timeoutIntervalForRequest = 15
        self.session = URLSession(configuration: cfg)
        self.task = session.webSocketTask(with: URL(string: server)!, protocols: ["etunnel-v1"])
    }

    func connect(token: String? = nil, subdomain: String? = nil) async throws -> String {
        task.resume()
        var hello: [String: Any] = ["type": "hello", "proto": 1, "client": "etunnel-swift/1.0"]
        if let t = token { hello["token"] = t }
        if let s = subdomain { hello["subdomain"] = s }
        let data = try JSONSerialization.data(withJSONObject: hello)
        try await task.send(.string(String(data: data, encoding: .utf8)!))
        for _ in 0..<30 {
            let msg = try await task.receive()
            if case .string(let text) = msg,
               let d = text.data(using: .utf8),
               let obj = try? JSONSerialization.jsonObject(with: d) as? [String: Any],
               let type = obj["type"] as? String {
                if type == "welcome" {
                    startLoop()
                    return obj["url"] as! String
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
        while true {
            guard let msg = try? await task.receive() else { return }
            switch msg {
            case .string(let text):
                guard let d = text.data(using: .utf8),
                      let obj = (try? JSONSerialization.jsonObject(with: d)) as? [String: Any],
                      let type = obj["type"] as? String else { continue }
                switch type {
                case "req":
                    let sid = UInt32(obj["streamId"] as! Int)
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
            let http = resp as! HTTPURLResponse
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
                "headers": ["Content-Type": ["text/plain"]]]
            if let d = try? JSONSerialization.data(withJSONObject: res),
               let s = String(data: d, encoding: .utf8) {
                try? await task.send(.string(s))
            }
            let msg = "etunnel: local service error: \(error.localizedDescription)\n"
            try? await task.send(.data(Data(frame(0x01, sid, Array(msg.utf8)))))
            try? await task.send(.data(Data(frame(0x02, sid, []))))
        }
    }

    func close() {
        Task { try? await task.send(.string("{\"type\":\"bye\",\"reason\":\"client closed\"}")) }
        task.cancel(with: .normalClosure, reason: nil)
    }
}

func log(_ s: String) { FileHandle.standardError.write((s + "\n").data(using: .utf8)!) }

let server = ProcessInfo.processInfo.environment["ETUNNEL_SERVER"] ?? "wss://t.ggcode.uk/tunnel"
let local = CommandLine.arguments.count > 1 ? CommandLine.arguments[1]
    : ProcessInfo.processInfo.environment["ETUNNEL_LOCAL"] ?? "3000"
let et = Etunnel(server: server, local: local)
do {
    let url = try await et.connect(
        token: ProcessInfo.processInfo.environment["ETUNNEL_TOKEN"],
        subdomain: ProcessInfo.processInfo.environment["ETUNNEL_SUBDOMAIN"])
    log("TUNNEL_URL: \(url)")
    while true { try await Task.sleep(nanoseconds: 1_000_000_000) }
} catch {
    log("etunnel: \(error)")
    exit(1)
}
