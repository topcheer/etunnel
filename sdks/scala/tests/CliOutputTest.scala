// TUNNEL_URL stdout-line regression (docs audit). The one-shot CLI path
// (ETUNNEL_RECONNECT=0) printed the Tunnel case class whole:
//   TUNNEL_URL: Tunnel(http://<sub>.t.example.com, <resumeToken>)
// which broke every TUNNEL_URL parser and leaked the server-signed resume
// token to stdout. Both CLI paths now go through Etunnel.tunnelUrlLine.
//
// Run (from sdks/scala; the scala-cli based `scala` runner drops into a REPL
// for `-cp X Main`, so use java with the two Scala library jars):
//   scalac -d /tmp/scala-out Etunnel.scala tests/CliOutputTest.scala
//   java -cp "/tmp/scala-out:<scala3-library_3.jar>:<scala-library.jar>" dev.ggcode.etunnel.CliOutputTest
// (both jars resolve with: coursier fetch -p org.scala-lang:scala3-library_3:3.10.1 org.scala-lang:scala-library:2.13.16)
package dev.ggcode.etunnel

object CliOutputTest {
  def main(args: Array[String]): Unit = {
    var fails = 0
    def ck(name: String, cond: Boolean): Unit = if (!cond) { fails += 1; println(s"FAIL $name") }

    val line = Etunnel.tunnelUrlLine(Etunnel.Tunnel("http://breezy-lynx.localhost", "SECRET-RESUME-TOKEN"))
    ck("exact line: prefix + bare URL", line == "TUNNEL_URL: http://breezy-lynx.localhost")
    ck("no case-class toString", !line.contains("Tunnel("))
    ck("resume token not leaked to stdout", !line.contains("SECRET"))
    ck("single line", !line.contains("\n"))

    // Both CLI paths must route through the helper - guards against a
    // copy-paste of the old `println(s"TUNNEL_URL: $tunnel")` coming back.
    val src = scala.io.Source.fromFile("Etunnel.scala").mkString
    ck("one-shot path uses tunnelUrlLine", src.contains("println(Etunnel.tunnelUrlLine(tunnel))"))
    ck("TUNNEL_URL is interpolated in exactly one place (the helper)",
      "\"TUNNEL_URL: \\$\\{".r.findAllIn(src).size == 1)

    if (fails > 0) { println(s"scala cli-output tests: $fails FAILED"); sys.exit(1) }
    println("scala cli-output tests: PASS")
  }
}
