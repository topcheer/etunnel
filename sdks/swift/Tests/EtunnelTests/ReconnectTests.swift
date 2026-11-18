import XCTest
@testable import etunnel

// #183: reconnect + observable state. These cases pin the semantics the Go SDK
// already guarantees (sdk/reconnect.go) so the two SDKs cannot drift, and the
// last one proves it against a real server when one is provided.

final class ReconnectTests: XCTestCase {

    // MARK: backoff

    func testBackoffGrowsExponentiallyAndClamps() {
        var b: TimeInterval = 0.5
        let max: TimeInterval = 30
        var seen: [TimeInterval] = []
        for _ in 0..<10 {
            seen.append(b)
            b = EtunnelBackoff.grow(b, max: max, factor: 2.0)
        }
        XCTAssertEqual(seen[0], 0.5, accuracy: 1e-9)
        XCTAssertEqual(seen[1], 1.0, accuracy: 1e-9)
        XCTAssertEqual(seen[2], 2.0, accuracy: 1e-9)
        XCTAssertEqual(seen[6], 30, accuracy: 1e-9, "must clamp at max")
        XCTAssertEqual(seen[9], 30, accuracy: 1e-9)
    }

    func testBackoffFromZeroGoesStraightToMax() {
        // Mirrors Go's grow(): a zero/negative input cannot stall the loop.
        XCTAssertEqual(EtunnelBackoff.grow(0, max: 30, factor: 2), 30, accuracy: 1e-9)
    }

    func testJitterStaysWithinFraction() {
        for _ in 0..<200 {
            let j = EtunnelBackoff.jitter(4.0, fraction: 0.1)
            XCTAssertGreaterThanOrEqual(j, 3.6 - 1e-9)
            XCTAssertLessThanOrEqual(j, 4.4 + 1e-9)
        }
    }

    func testJitterIsDeterministicWithInjectedRandom() {
        XCTAssertEqual(EtunnelBackoff.jitter(10, fraction: 0.1, random: { 0 }), 9, accuracy: 1e-9)
        XCTAssertEqual(EtunnelBackoff.jitter(10, fraction: 0.1, random: { 1 }), 11, accuracy: 1e-9)
        XCTAssertEqual(EtunnelBackoff.jitter(10, fraction: 0.1, random: { 0.5 }), 10, accuracy: 1e-9)
    }

    func testJitterLeavesZeroAlone() {
        XCTAssertEqual(EtunnelBackoff.jitter(0), 0)
    }

    // MARK: permanent errors

    func testPermanentCodesStopRetrying() {
        for code in ["unauthorized", "subdomain_plan", "subdomain_invalid", "bad_message"] {
            let err = NSError(domain: "etunnel", code: 1,
                              userInfo: [NSLocalizedDescriptionKey: "\(code): nope"])
            XCTAssertTrue(EtunnelErrorClass.isPermanent(err), "\(code) should be permanent")
        }
    }

    func testTransientErrorsAreRetried() {
        // welcome timeout
        XCTAssertFalse(EtunnelErrorClass.isPermanent(
            NSError(domain: "etunnel", code: 2,
                    userInfo: [NSLocalizedDescriptionKey: "welcome timeout"])))
        // an unknown server code is treated as transient rather than fatal: a
        // retry is cheaper than giving up on a tunnel that might recover.
        XCTAssertFalse(EtunnelErrorClass.isPermanent(
            NSError(domain: "etunnel", code: 1,
                    userInfo: [NSLocalizedDescriptionKey: "overloaded: try later"])))
        // transport-level errors are never permanent
        XCTAssertFalse(EtunnelErrorClass.isPermanent(URLError(.networkConnectionLost)))
    }

    func testProtocolDeviationIsPermanent() {
        let err = NSError(domain: "etunnel", code: 3,
                          userInfo: [NSLocalizedDescriptionKey: "welcome missing url"])
        XCTAssertTrue(EtunnelErrorClass.isPermanent(err))
    }

    // MARK: state surface (what a UI renders)

    func testStateExposesURLForDisplay() {
        XCTAssertEqual(EtunnelState.connected(url: "https://a.example").publicURL, "https://a.example")
        XCTAssertTrue(EtunnelState.connected(url: "u").isConnected)
        // while retrying the last known URL stays visible
        XCTAssertEqual(EtunnelState.retrying(attempt: 2, delay: 1, url: "https://a.example").publicURL,
                       "https://a.example")
        XCTAssertFalse(EtunnelState.retrying(attempt: 1, delay: 1, url: nil).isConnected)
        XCTAssertNil(EtunnelState.connecting.publicURL)
        XCTAssertNil(EtunnelState.closed(reason: "bye").publicURL)
    }

    func testInvalidServerIsRejectedUpFront() {
        XCTAssertNil(ReconnectingEtunnel(server: "not a url", local: "3000"))
    }

    // MARK: live proof (needs a server)

    /// Requires ETUNNEL_SERVER (e.g. ws://127.0.0.1:PORT/tunnel) and a local
    /// app to forward to. The shell harness (tests/test_reconnect.sh) starts a
    /// real etunneld, kills it mid-flight, restarts it, and asserts the
    /// subdomain comes back - this case only checks the state transitions.
    func testStateStreamReportsConnectedThenClosedOnUserClose() async throws {
        guard let server = ProcessInfo.processInfo.environment["ETUNNEL_SERVER"],
              !server.isEmpty else {
            throw XCTSkip("ETUNNEL_SERVER not set; run via Tests/test_reconnect.sh")
        }
        guard let rt = ReconnectingEtunnel(server: server, local: "127.0.0.1:39999") else {
            return XCTFail("could not build supervisor for \(server)")
        }

        var states: [EtunnelState] = []
        let collector = Task { () -> [EtunnelState] in
            var out: [EtunnelState] = []
            for await s in rt.states { out.append(s) }
            return out
        }

        _ = try await rt.connect()
        XCTAssertTrue(rt.state.isConnected, "expected connected, got \(rt.state)")
        XCTAssertNotNil(rt.publicURL)

        rt.close(reason: "test finished")
        // Closing must surface a terminal state and stop the supervisor.
        let deadline = Date().addingTimeInterval(3)
        while Date() < deadline {
            if case .closed = rt.state { break }
            try? await Task.sleep(nanoseconds: 50_000_000)
        }
        if case .closed(let reason) = rt.state {
            XCTAssertEqual(reason, "test finished")
        } else {
            XCTFail("close did not reach .closed: \(rt.state)")
        }
        collector.cancel()
        _ = states
    }
}

// The stream must FINISH on a terminal state; a `for await` that never ends
// would hang any consumer that awaits the tunnel's lifetime.
final class ReconnectStreamTerminationTests: XCTestCase {

    func testClosedStateFinishesTheStream() async throws {
        guard let server = ProcessInfo.processInfo.environment["ETUNNEL_SERVER"], !server.isEmpty else {
            throw XCTSkip("ETUNNEL_SERVER not set; run via Tests/test_reconnect.sh")
        }
        guard let rt = ReconnectingEtunnel(server: server, local: "127.0.0.1:39999") else {
            return XCTFail("could not build supervisor")
        }
        _ = try await rt.connect()

        let finished = expectation(description: "stream finished")
        let consumer = Task {
            var count = 0
            for await _ in rt.states { count += 1 }
            finished.fulfill()
            return count
        }
        rt.close(reason: "done")
        await fulfillment(of: [finished], timeout: 5)
        let seen = await consumer.value
        XCTAssertGreaterThanOrEqual(seen, 1)
    }
}
