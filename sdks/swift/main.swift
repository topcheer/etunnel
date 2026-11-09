// etunnel CLI entry point. Run: swift run etunnel 3000
//
// The client itself lives in etunnel.swift (a single file with no other
// dependencies); this file only wires it to the command line.

import Foundation

func log(_ s: String) { FileHandle.standardError.write((s + "\n").data(using: .utf8)!) }

let server = ProcessInfo.processInfo.environment["ETUNNEL_SERVER"] ?? "wss://t.etun.uk/tunnel"
let local = CommandLine.arguments.count > 1 ? CommandLine.arguments[1]
    : ProcessInfo.processInfo.environment["ETUNNEL_LOCAL"] ?? "3000"
// ETUNNEL_RECONNECT=0 restores the one-shot behaviour (exit on first error).
let reconnect = (ProcessInfo.processInfo.environment["ETUNNEL_RECONNECT"] ?? "1") != "0"

/// Set from the SIGINT/SIGTERM dispatch sources; the supervisor stops as soon
/// as it is true and never reconnects afterwards.
final class StopFlag: @unchecked Sendable {
    private let lock = NSLock()
    private var stopped = false
    var isStopped: Bool {
        lock.lock()
        defer { lock.unlock() }
        return stopped
    }
    func stop() {
        lock.lock()
        stopped = true
        lock.unlock()
    }
}

let stop = StopFlag()
var signalSources: [DispatchSourceSignal] = []
for sig in [SIGINT, SIGTERM] {
    signal(sig, SIG_IGN)
    let src = DispatchSource.makeSignalSource(signal: sig, queue: .global())
    src.setEventHandler { stop.stop() }
    src.resume()
    signalSources.append(src)
}

/// 500ms, 1s, 2s, ... capped at 30s, then ±20% jitter.
func backoffMs(_ attempt: Int) -> UInt64 {
    let base = min(30_000.0, 500.0 * pow(2.0, Double(min(attempt, 6))))
    return UInt64((base * Double.random(in: 0.8...1.2)).rounded())
}

var attempt = 0
// Carried across drops so a reconnect reclaims the same subdomain.
var resume: String? = nil
while !stop.isStopped {
    let et = Etunnel(server: server, local: local)
    do {
        let url = try await et.connect(
            token: ProcessInfo.processInfo.environment["ETUNNEL_TOKEN"],
            subdomain: ProcessInfo.processInfo.environment["ETUNNEL_SUBDOMAIN"],
            resume: resume)
        log("TUNNEL_URL: \(url)")
        if !et.resumeToken.isEmpty { resume = et.resumeToken }
        attempt = 0
        if !reconnect {
            while true { try await Task.sleep(nanoseconds: 1_000_000_000) }
        }
        while !stop.isStopped && !et.isClosed {
            try await Task.sleep(nanoseconds: 200_000_000)
        }
        et.close()
        if stop.isStopped { break }
        log("etunnel: connection lost, reconnecting")
    } catch {
        if stop.isStopped { break }
        log("etunnel: \(error)")
        if !reconnect {
            exit(1)
        }
    }
    let ms = backoffMs(attempt)
    attempt += 1
    log("etunnel: reconnect in \(ms)ms")
    let deadline = Date().addingTimeInterval(Double(ms) / 1000)
    while !stop.isStopped && Date() < deadline {
        try await Task.sleep(nanoseconds: 100_000_000)
    }
}
