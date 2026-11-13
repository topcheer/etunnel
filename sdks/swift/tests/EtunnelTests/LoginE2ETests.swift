import XCTest
@testable import etunnel

// E2E against the shared Go loginserver fixture. The shell test
// (tests/test_login.sh) starts the fixture, exports FX_ADDR, and runs
// `swift test --filter LoginE2E`. Skips when FX_ADDR is unset (plain
// `swift test` on CI without the fixture).
final class LoginE2ETests: XCTestCase {
    func testLoopbackLoginStartGrantExchange() throws {
        guard let addr = ProcessInfo.processInfo.environment["FX_ADDR"], !addr.isEmpty else {
            throw XCTSkip("FX_ADDR not set; run via tests/test_login.sh")
        }
        let creds = try etunnelLogin("http://" + addr, openBrowser: { url in
            _ = try? Process.run(URL(fileURLWithPath: "/usr/bin/curl"), arguments: ["-s", "-o", "/dev/null", url])
        }, timeout: 30)
        XCTAssertEqual(creds.apiToken, "tok-123")
        XCTAssertEqual(creds.subdomains, ["my-sub"])
        XCTAssertEqual(creds.plan, "pro")
    }
}
