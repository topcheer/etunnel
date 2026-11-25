// #338: proto gate tests - welcome.proto > 1 rejected loudly; missing proto
// counts as v1 (PROTOCOL.md section 3 both-sides contract).
// Run: kotlinc Etunnel.kt tests/ProtoGateTest.kt -include-runtime -d pg.jar && java -jar pg.jar
package dev.ggcode.etunnel

import java.net.http.HttpClient

fun main() {
    var fails = 0
    fun ck(name: String, got: Any?, want: Any?) {
        val g = got?.toString() ?: "null"
        val w = want?.toString() ?: "null"
        if (g != w) { fails++; println("FAIL $name: got $g want $w") }
    }

    val h = Etunnel.Companion.Handler(HttpClient.newHttpClient(), 3000, null)
    h.handleText("""{"type":"welcome","proto":2,"url":"https://x.t.test"}""")
    ck("proto=2 rejected with proto_version", h.error?.contains("proto_version"), true)

    val h1 = Etunnel.Companion.Handler(HttpClient.newHttpClient(), 3000, null)
    h1.handleText("""{"type":"welcome","url":"https://x.t.test"}""")
    ck("missing proto defaults to v1 (accepted)", h1.info != null, true)
    ck("v1 welcome stores url", h1.info?.get("url"), "https://x.t.test")

    if (fails == 0) println("kotlin proto gate tests: PASS")
    else { println("kotlin proto gate tests: FAIL ($fails)"); kotlin.system.exitProcess(1) }
}
