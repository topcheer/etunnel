// etunnel CLI entry point. Run: swift run etunnel-cli 3000
//
// The client itself lives in etunnel.swift; the supervisor lives in
// Reconnect.swift. This file only wires them to the command line.
//
// Until #183 the retry loop was hand-written here - exactly the burden the SDK
// should carry. It now uses ReconnectingEtunnel, so the CLI and any app share
// one supervisor (and one set of reconnect semantics with the Go SDK).

import Foundation
import etunnel

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

guard let tunnel = ReconnectingEtunnel(server: server, local: local) else {
    log("etunnel: invalid server URL: \(server)")
    exit(2)
}

// Log every transition: operators watch stderr, and the shell E2E asserts on it.
tunnel.onState = { state in
    switch state {
    case .connecting:
        break // the initial dial logs its own URL below
    case .connected(let url):
        log("TUNNEL_URL: \(url)")
    case .retrying(let attempt, let delay, let url):
        let live = url.map { " (last url \($0))" } ?? ""
        log("etunnel: connection lost, retry \(attempt) in \(String(format: "%.1f", delay))s\(live)")
    case .closed(let reason):
        log("etunnel: stopped: \(reason)")
    }
}
tunnel.onReconnect = { attempt, url in
    log("etunnel: reconnected after \(attempt) attempt(s)")
}

do {
    // onState(.connected) already logs TUNNEL_URL - logging it again here made
    // the first URL appear twice, which the E2E (and any log scraper) misread
    // as an immediate reconnect.
    _ = try await tunnel.connect(
        token: ProcessInfo.processInfo.environment["ETUNNEL_TOKEN"],
        subdomain: ProcessInfo.processInfo.environment["ETUNNEL_SUBDOMAIN"])
} catch {
    log("etunnel: \(error)")
    exit(1)
}

if !reconnect {
    // One-shot mode: hold the connection but never let the supervisor retry.
    // (Close on stop so SIGINT does not hang; same class as the C# hang, #54.)
    while !stop.isStopped && !(tunnel.tunnel?.isClosed ?? true) {
        try await Task.sleep(nanoseconds: 200_000_000)
    }
    tunnel.close(reason: "one-shot exit")
    exit(tunnel.state.isConnected ? 0 : 1)
}

// Supervised mode: idle until a signal. ReconnectingEtunnel owns the retries.
while !stop.isStopped {
    try await Task.sleep(nanoseconds: 200_000_000)
}
tunnel.close(reason: "signal")
