// Reconnect.swift — a supervising wrapper that keeps a tunnel alive across
// disconnects, plus the state stream UI layers need (issue #183).
//
// Motivation: `Etunnel.connect()` is one-shot. When the socket drops the caller
// had to hand-roll a retry loop and could only infer liveness from
// `publicURL == nil`, so the first iOS consumer (Burrow) wrote its own
// supervisor and its preview button had no reliable signal to render.
//
// This mirrors the Go SDK's sdk/reconnect.go so both languages behave the same:
//   * first dial fails fast (bad credentials must not loop forever)
//   * a drop that we did not ask for is retried with exponential backoff
//     (0.5s -> 30s, x2, +-10% jitter)
//   * the last resume token is replayed so the client keeps its subdomain
//   * backoff resets after a success
//   * Close() never reconnects
//   * errors that retrying cannot fix (bad token, plan, protocol) stop the loop
//
// The single-connection `Etunnel` is untouched: this wrapper owns the retry
// policy, so a bug here cannot corrupt the data path.

import Foundation

// MARK: - state

/// What the supervisor is doing. Delivered to `states` and `onState` so a UI
/// can map tunnel liveness onto its own status instead of guessing.
public enum EtunnelState: Equatable, Sendable {
    /// First dial in flight.
    case connecting
    /// Tunnel is up. `url` is the public URL to show.
    case connected(url: String)
    /// The socket dropped and we are about to wait `delay` seconds before
    /// attempt number `attempt` (1-based). `url` is the last known public URL,
    /// which stays valid for display during the outage.
    case retrying(attempt: Int, delay: TimeInterval, url: String?)
    /// Deliberately stopped: user close, permanent error, or exhausted retries.
    /// `reason` is a human-readable explanation, never nil in practice.
    case closed(reason: String)

    public var isConnected: Bool {
        if case .connected = self { return true }
        return false
    }

    /// The URL worth showing: live when connected, last-known while retrying.
    public var publicURL: String? {
        switch self {
        case .connected(let url): return url
        case .retrying(_, _, let url): return url
        case .connecting, .closed: return nil
        }
    }
}

// MARK: - policy

public struct EtunnelReconnectPolicy: Sendable {
    public var minBackoff: TimeInterval
    public var maxBackoff: TimeInterval
    public var factor: Double
    /// Fraction of the delay applied as +/- jitter (0.1 = +-10%).
    public var jitterFraction: Double
    /// Give up after this many failed attempts in a row (0 = never give up).
    public var maxAttempts: Int

    public init(minBackoff: TimeInterval = 0.5,
                maxBackoff: TimeInterval = 30,
                factor: Double = 2.0,
                jitterFraction: Double = 0.1,
                maxAttempts: Int = 0) {
        self.minBackoff = minBackoff
        self.maxBackoff = maxBackoff
        self.factor = factor
        self.jitterFraction = jitterFraction
        self.maxAttempts = maxAttempts
    }
}

/// Pure backoff arithmetic (unit-tested; mirrors grow() in the Go SDK).
public enum EtunnelBackoff {
    public static func grow(_ backoff: TimeInterval, max: TimeInterval, factor: Double) -> TimeInterval {
        var next = backoff * factor
        if next > max { next = max }
        if next <= 0 { next = max }
        return next
    }

    /// Spreads reconnect storms: `delay` plus a uniform offset inside
    /// +/-`fraction` of it. Deterministic when `random` is injected.
    public static func jitter(_ delay: TimeInterval,
                              fraction: Double = 0.1,
                              random: () -> Double = { Double.random(in: 0...1) }) -> TimeInterval {
        guard delay > 0, fraction > 0 else { return delay }
        let spread = delay * fraction
        return delay - spread + random() * (2 * spread)
    }
}

// MARK: - error classification

/// Errors that retrying cannot fix. The server reports them as
/// `{"type":"error","code":"...","message":"..."}`; `connect()` surfaces that
/// as NSError(domain: "etunnel", code: 1, message: "<code>: <message>").
public enum EtunnelErrorClass {
    public static let permanentCodes: Set<String> = [
        "unauthorized", "subdomain_plan", "subdomain_bad", "subdomain_invalid",
        "bad_message", "unsupported_proto",
    ]

    /// True when a dial error is terminal (retrying would loop forever).
    public static func isPermanent(_ error: Error) -> Bool {
        let ns = error as NSError
        guard ns.domain == "etunnel" else { return false }
        switch ns.code {
        case 3:
            // welcome missing url: the server deviates from the protocol.
            return true
        case 1:
            // "<code>: <message>" - check the code the server sent.
            let message = ns.localizedDescription
            guard let colon = message.firstIndex(of: ":") else { return false }
            let code = message[..<colon].trimmingCharacters(in: .whitespaces)
            return permanentCodes.contains(code)
        default:
            return false
        }
    }

    /// Human-readable reason for a closed state.
    public static func describe(_ error: Error) -> String {
        let ns = error as NSError
        if ns.domain == "etunnel" {
            switch ns.code {
            case 1: return "server refused: \(ns.localizedDescription)"
            case 2: return "no welcome from server (timeout)"
            case 3: return "protocol error: \(ns.localizedDescription)"
            default: return ns.localizedDescription
            }
        }
        return ns.localizedDescription
    }
}

// MARK: - supervisor

/// A tunnel that survives disconnects. Every accessor proxies to the current
/// underlying `Etunnel`; a fresh `Etunnel` is built for each attempt because a
/// `URLSessionWebSocketTask` cannot be resumed twice.
public final class ReconnectingEtunnel: @unchecked Sendable {
    private let server: String
    private let local: String
    private let routes: [EtunnelRoute]?
    private let policy: EtunnelReconnectPolicy

    private let lock = NSLock()
    private var current: Etunnel?
    private var lastURL: String?
    private var lastResume: String = ""
    private var reconnectCount = 0
    private var userClosed = false
    private var supervisorTask: Task<Void, Never>?
    private var continuation: AsyncStream<EtunnelState>.Continuation?
    private var lastState: EtunnelState = .connecting

    /// Called on every state change (set before `connectReconnecting` to avoid
    /// missing the first transition). Always invoked on the main actor when
    /// available, so a UI can assign straight to `@Published` state.
    public var onState: (@Sendable (EtunnelState) -> Void)?
    /// Called after each successful reconnect with (attempt, url).
    public var onReconnect: (@Sendable (Int, String) -> Void)?
    /// Optional log sink.
    public var onLog: (@Sendable (String) -> Void)?

    public init?(server: String, local: String, routes: [EtunnelRoute]? = nil,
                 policy: EtunnelReconnectPolicy = EtunnelReconnectPolicy()) {
        // Validate once up front using a throwaway instance so a bad URL or
        // route table fails here rather than on every retry.
        guard Etunnel(server: server, local: local, routes: routes) != nil else { return nil }
        self.server = server
        self.local = local
        self.routes = routes
        self.policy = policy
    }

    deinit {
        // Cancel first so nothing starts another dial, then close the live
        // socket: dropping the last reference to a connected supervisor used to
        // leave the tunnel running (the URLSession keeps the task alive).
        supervisorTask?.cancel()
        lock.lock()
        let cur = current
        current = nil
        lock.unlock()
        cur?.close()
    }

    // MARK: observation

    /// Async sequence of states. Finishes when the supervisor stops.
    public var states: AsyncStream<EtunnelState> {
        AsyncStream { continuation in
            lock.lock()
            let snapshot = lastState
            let alreadyClosed: Bool
            if case .closed = snapshot { alreadyClosed = true } else { alreadyClosed = false }
            if !alreadyClosed { self.continuation = continuation }
            lock.unlock()
            continuation.yield(snapshot)
            // Subscribing AFTER the supervisor stopped must not hang: hand back
            // the terminal state and finish immediately.
            if alreadyClosed {
                continuation.finish()
                return
            }
            continuation.onTermination = { [weak self] _ in
                guard let self else { return }
                self.lock.lock()
                self.continuation = nil
                self.lock.unlock()
            }
        }
    }

    /// Latest state, synchronously.
    public var state: EtunnelState {
        lock.lock()
        defer { lock.unlock() }
        return lastState
    }

    /// Public URL to display: live when connected, last known while retrying.
    public var publicURL: String? {
        lock.lock()
        defer { lock.unlock() }
        if case .connected(let url) = lastState { return url }
        return lastURL
    }

    /// How many successful reconnects have happened.
    public var attempts: Int {
        lock.lock()
        defer { lock.unlock() }
        return reconnectCount
    }

    /// The live underlying tunnel, or nil while a reconnect is in flight.
    public var tunnel: Etunnel? {
        lock.lock()
        defer { lock.unlock() }
        return current
    }

    // MARK: lifecycle

    /// Dial once (failing fast) and then supervise in the background.
    /// Returns the initial public URL.
    @discardableResult
    public func connect(token: String? = nil,
                        subdomain: String? = nil,
                        resume: String? = nil) async throws -> String {
        emit(.connecting)
        let first: Etunnel
        let firstURL: String
        do {
            (first, firstURL) = try await dial(token: token, subdomain: subdomain, resume: resume)
        } catch {
            emit(.closed(reason: EtunnelErrorClass.describe(error)))
            throw error
        }
        lock.lock()
        current = first
        lastURL = firstURL
        lastResume = first.resumeToken
        lock.unlock()
        emit(.connected(url: firstURL))

        startSupervisor(token: token, subdomain: subdomain)
        return firstURL
    }

    /// Stop supervising and close the tunnel. Never reconnects.
    public func close(reason: String = "client closed") {
        lock.lock()
        guard !userClosed else { lock.unlock(); return }
        userClosed = true
        let cur = current
        lock.unlock()

        supervisorTask?.cancel()
        supervisorTask = nil
        cur?.close()
        emit(.closed(reason: reason))
    }

    // MARK: internals

    /// Build a fresh tunnel and dial it. Reusing an `Etunnel` is impossible:
    /// the underlying WebSocket task is single-use.
    private func dial(token: String?, subdomain: String?, resume: String?) async throws -> (Etunnel, String) {
        guard let fresh = Etunnel(server: server, local: local, routes: routes) else {
            throw NSError(domain: "etunnel", code: 4,
                          userInfo: [NSLocalizedDescriptionKey: "invalid server or route table"])
        }
        // connect() returns the public URL; the tunnel does not store it, so the
        // supervisor keeps it (that is what the UI displays while reconnecting).
        let url = try await fresh.connect(token: token, subdomain: subdomain, resume: resume)
        return (fresh, url)
    }

    /// Watch the current tunnel; when it ends without our involvement, retry.
    private func startSupervisor(token: String?, subdomain: String?) {
        supervisorTask = Task.detached { [weak self] in
            guard let self else { return }
            var backoff = max(self.policy.minBackoff, 0.001)
            var failures = 0

            while true {
                guard let cur = self.tunnelForSupervision() else { return }
                // Wait for the tunnel's receive loop to end.
                while !cur.isClosed {
                    if Task.isCancelled { return }
                    try? await Task.sleep(nanoseconds: 150_000_000)
                }
                if self.isUserClosed() || Task.isCancelled { return }

                // Loop until reconnected, a permanent error, or a user close.
                while true {
                    if self.isUserClosed() || Task.isCancelled { return }
                    failures += 1

                    let delay = EtunnelBackoff.jitter(backoff, fraction: self.policy.jitterFraction)
                    let attempt = failures
                    self.log("connection lost; reconnecting in \(String(format: "%.1f", delay))s (attempt \(attempt))")
                    self.emit(.retrying(attempt: attempt, delay: delay, url: self.lastKnownURL()))

                    if self.policy.maxAttempts > 0 && failures > self.policy.maxAttempts {
                        self.emit(.closed(reason: "gave up after \(self.policy.maxAttempts) reconnect attempts"))
                        return
                    }
                    // Cancellable sleep: a user close during the wait must win.
                    let deadline = Date().addingTimeInterval(delay)
                    while Date() < deadline {
                        if self.isUserClosed() || Task.isCancelled { return }
                        try? await Task.sleep(nanoseconds: 100_000_000)
                    }

                    do {
                        // Replay the resume token so the subdomain is reclaimed
                        // instead of a new random one being issued.
                        let (next, nextURL) = try await self.dial(
                            token: token, subdomain: subdomain,
                            resume: self.resumeForReconnect())
                        self.lock.lock()
                        self.current = next
                        self.lastURL = nextURL
                        self.lastResume = next.resumeToken
                        self.reconnectCount += 1
                        let url = nextURL
                        let attemptNo = self.reconnectCount
                        self.lock.unlock()
                        self.onReconnect?(attemptNo, url)
                        self.emit(.connected(url: url))
                        self.log("reconnected after \(failures) attempt(s): \(url)")
                        backoff = max(self.policy.minBackoff, 0.001) // reset on success
                        failures = 0
                        break
                    } catch {
                        if EtunnelErrorClass.isPermanent(error) {
                            self.log("reconnect refused permanently: \(error.localizedDescription)")
                            self.emit(.closed(reason: EtunnelErrorClass.describe(error)))
                            return
                        }
                        self.log("reconnect failed: \(error.localizedDescription)")
                        backoff = EtunnelBackoff.grow(backoff,
                                                      max: self.policy.maxBackoff,
                                                      factor: self.policy.factor)
                    }
                }
            }
        }
    }

    private func tunnelForSupervision() -> Etunnel? {
        lock.lock()
        defer { lock.unlock() }
        return current
    }

    private func resumeForReconnect() -> String? {
        lock.lock()
        defer { lock.unlock() }
        return lastResume.isEmpty ? nil : lastResume
    }

    private func lastKnownURL() -> String? {
        lock.lock()
        defer { lock.unlock() }
        return lastURL
    }

    private func isUserClosed() -> Bool {
        lock.lock()
        defer { lock.unlock() }
        return userClosed
    }

    private func log(_ message: String) {
        onLog?("etunnel[reconnect]: \(message)")
    }

    private func emit(_ state: EtunnelState) {
        lock.lock()
        lastState = state
        let cont = continuation
        if case .closed = state {
            continuation = nil // a finished stream cannot be reused
        }
        lock.unlock()
        cont?.yield(state)
        // Finish the sequence on a terminal state: otherwise a consumer doing
        // `for await state in tunnel.states` would wait forever after close.
        if case .closed = state {
            cont?.finish()
        }
        onState?(state)
    }
}
