// Route table tests (same semantics table as sdks/node/route.js).
fun main() {
    var fails = 0
    fun ck(name: String, got: Any?, want: Any?) {
        if (got.toString() != want.toString()) {
            fails++; println("FAIL $name: got $got want $want")
        }
    }
    fun msg(body: () -> Unit): String = try { body(); "accepted" } catch (e: IllegalArgumentException) { e.message ?: "?" }

    val rs = Etunnel.normalizeRoutes(
        listOf(Route("/", "fe:1"), Route("/api", "be:1"), Route("/api/v2", "be2:1")))
    ck("/apiv2 -> /", Etunnel.routeFor(rs, "/apiv2")?.first, "fe:1")
    ck("/apiary -> /", Etunnel.routeFor(rs, "/apiary")?.first, "fe:1")
    ck("/api exact", Etunnel.routeFor(rs, "/api")?.first, "be:1")
    ck("/api/u", Etunnel.routeFor(rs, "/api/u")?.first, "be:1")
    ck("longest wins", Etunnel.routeFor(rs, "/api/v2/x?q")?.first, "be2:1")

    val ss = Etunnel.normalizeRoutes(
        listOf(Route("/api", "be:1", true), Route("/", "fe:1", true)))
    ck("strip keeps query", Etunnel.routeFor(ss, "/api/u?x=1&y=2")?.second, "/u?x=1&y=2")
    ck("exact strip -> /", Etunnel.routeFor(ss, "/api")?.second, "/")
    ck("strip root prefix = original", Etunnel.routeFor(ss, "/app?c=1")?.second, "/app?c=1")

    val one = Etunnel.normalizeRoutes(listOf(Route("/api", "be:1")))
    ck("no match -> null", Etunnel.routeFor(one, "/other"), null)

    ck("addr required", msg { Etunnel.normalizeRoutes(listOf(Route("/", ""))) }.contains("addr is required"), true)
    ck("must start with /", msg { Etunnel.normalizeRoutes(listOf(Route("api", "a:1"))) }.contains("must start with"), true)
    ck("duplicate", msg { Etunnel.normalizeRoutes(listOf(Route("/api", "a:1"), Route("/api/", "a:2"))) }.contains("duplicate"), true)

    if (fails == 0) println("kotlin route tests: PASS")
    else { println("kotlin route tests: FAIL ($fails)"); kotlin.system.exitProcess(1) }
}
