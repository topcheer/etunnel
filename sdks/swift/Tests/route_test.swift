// Route table tests (same semantics table as sdks/node/route.js).
// Build: swiftc -o /tmp/et-swift-rt etunnel.swift tests/route_test.swift && /tmp/et-swift-rt
import Foundation

@main
struct RouteTestMain {
    static var failures = 0

    static func ck(_ name: String, _ got: String, _ want: String) {
        if got != want { failures += 1; print("FAIL \(name): got \(got) want \(want)") }
    }

    static func msg(_ body: () throws -> Void) -> String {
        do { try body(); return "accepted" }
        catch { return (error as NSError).localizedDescription }
    }

    static func main() {
        do {
            let rs = try etunnelNormalizeRoutes([
                EtunnelRoute(prefix: "/", addr: "fe:1"),
                EtunnelRoute(prefix: "/api", addr: "be:1"),
                EtunnelRoute(prefix: "/api/v2", addr: "be2:1"),
            ])
            ck("/apiv2 -> /", etunnelRouteFor(rs, "/apiv2")!.addr, "fe:1")
            ck("/apiary -> /", etunnelRouteFor(rs, "/apiary")!.addr, "fe:1")
            ck("/api exact", etunnelRouteFor(rs, "/api")!.addr, "be:1")
            ck("/api/u", etunnelRouteFor(rs, "/api/u")!.addr, "be:1")
            ck("longest wins", etunnelRouteFor(rs, "/api/v2/x?q")!.addr, "be2:1")

            let ss = try etunnelNormalizeRoutes([
                EtunnelRoute(prefix: "/api", addr: "be:1", strip: true),
                EtunnelRoute(prefix: "/", addr: "fe:1", strip: true),
            ])
            ck("strip keeps query", etunnelRouteFor(ss, "/api/u?x=1&y=2")!.path, "/u?x=1&y=2")
            ck("exact strip -> /", etunnelRouteFor(ss, "/api")!.path, "/")
            ck("strip root prefix = original", etunnelRouteFor(ss, "/app?c=1")!.path, "/app?c=1")
        } catch {
            failures += 1
            print("FAIL normalize threw: \(error)")
        }

        let one = try! etunnelNormalizeRoutes([EtunnelRoute(prefix: "/api", addr: "be:1")])
        ck("no match -> nil", etunnelRouteFor(one, "/other") == nil ? "nil" : "hit", "nil")
        ck("addr required", msg { _ = try etunnelNormalizeRoutes([EtunnelRoute(prefix: "/", addr: "")]) }.contains("addr is required") ? "rejected" : "accepted", "rejected")
        ck("must start with /", msg { _ = try etunnelNormalizeRoutes([EtunnelRoute(prefix: "api", addr: "a:1")]) }.contains("must start with") ? "rejected" : "accepted", "rejected")
        ck("duplicate", msg { _ = try etunnelNormalizeRoutes([EtunnelRoute(prefix: "/api", addr: "a:1"), EtunnelRoute(prefix: "/api/", addr: "a:2")]) }.contains("duplicate") ? "rejected" : "accepted", "rejected")

        if failures == 0 { print("swift route tests: PASS") } else { print("swift route tests: FAIL (\(failures))"); exit(1) }
    }
}
