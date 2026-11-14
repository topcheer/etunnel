// Route table tests (same semantics table as sdks/node/route.js).
object RouteTest {
  var fails = 0

  def ck(name: String, got: Any, want: Any): Unit =
    if (got.toString != want.toString) { fails += 1; println(s"FAIL $name: got $got want $want") }

  def msg(body: => Unit): String =
    try { body; "accepted" } catch { case e: IllegalArgumentException => e.getMessage }

  def main(args: Array[String]): Unit = {
    val rs = Etunnel.normalizeRoutes(Seq(
      Etunnel.Route("/", "fe:1"), Etunnel.Route("/api", "be:1"), Etunnel.Route("/api/v2", "be2:1")))
    ck("/apiv2 -> /", Etunnel.routeFor(rs, "/apiv2").map(_._1).orNull, "fe:1")
    ck("/apiary -> /", Etunnel.routeFor(rs, "/apiary").map(_._1).orNull, "fe:1")
    ck("/api exact", Etunnel.routeFor(rs, "/api").map(_._1).orNull, "be:1")
    ck("/api/u", Etunnel.routeFor(rs, "/api/u").map(_._1).orNull, "be:1")
    ck("longest wins", Etunnel.routeFor(rs, "/api/v2/x?q").map(_._1).orNull, "be2:1")

    val ss = Etunnel.normalizeRoutes(Seq(
      Etunnel.Route("/api", "be:1", true), Etunnel.Route("/", "fe:1", true)))
    ck("strip keeps query", Etunnel.routeFor(ss, "/api/u?x=1&y=2").map(_._2).orNull, "/u?x=1&y=2")
    ck("exact strip -> /", Etunnel.routeFor(ss, "/api").map(_._2).orNull, "/")
    ck("strip root prefix = original", Etunnel.routeFor(ss, "/app?c=1").map(_._2).orNull, "/app?c=1")

    val one = Etunnel.normalizeRoutes(Seq(Etunnel.Route("/api", "be:1")))
    ck("no match -> None", Etunnel.routeFor(one, "/other"), None)

    ck("addr required", msg(Etunnel.normalizeRoutes(Seq(Etunnel.Route("/", "")))).contains("addr is required"), true)
    ck("must start with /", msg(Etunnel.normalizeRoutes(Seq(Etunnel.Route("api", "a:1")))).contains("must start with"), true)
    ck("duplicate", msg(Etunnel.normalizeRoutes(Seq(Etunnel.Route("/api", "a:1"), Etunnel.Route("/api/", "a:2")))).contains("duplicate"), true)

    if (fails == 0) println("scala route tests: PASS")
    else { println(s"scala route tests: FAIL ($fails)"); sys.exit(1) }
  }
}
