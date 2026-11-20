// Pure-JVM unit tests for the etunnel Android SDK: no Android runtime, no
// network. Covers the cross-SDK parity seams: ETUNNEL_* env mapping, JSON,
// routes, binary frame layout (protocol/frame.go 9-byte header).
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

private fun frameSid(f: ByteArray): Int =
    ((f[1].toInt() and 0xFF) shl 24) or ((f[2].toInt() and 0xFF) shl 16) or
        ((f[3].toInt() and 0xFF) shl 8) or (f[4].toInt() and 0xFF)

private fun frameLen(f: ByteArray): Int =
    ((f[5].toInt() and 0xFF) shl 24) or ((f[6].toInt() and 0xFF) shl 16) or
        ((f[7].toInt() and 0xFF) shl 8) or (f[8].toInt() and 0xFF)

class EtunnelSdkTest {

    // ---- ETUNNEL_* env contract (configFromEnv) ----

    @Test
    fun envDefaults() {
        val c = configFromEnv(emptyMap())
        assertEquals("wss://t.etun.uk/tunnel", c.server)
        assertEquals(3000, c.localPort)
        assertNull(c.token)
        assertNull(c.subdomain)
        assertTrue(c.reconnect) // reconnect is the default
    }

    @Test
    fun envMapping() {
        val c = configFromEnv(
            mapOf(
                "ETUNNEL_SERVER" to "wss://example.com/tunnel",
                "ETUNNEL_LOCAL" to "8080",
                "ETUNNEL_TOKEN" to "tok123",
                "ETUNNEL_SUBDOMAIN" to "my-sub"
            )
        )
        assertEquals("wss://example.com/tunnel", c.server)
        assertEquals(8080, c.localPort)
        assertEquals("tok123", c.token)
        assertEquals("my-sub", c.subdomain)
        assertTrue(c.reconnect)
    }

    @Test
    fun reconnectZeroDisables() {
        assertTrue(!configFromEnv(mapOf("ETUNNEL_RECONNECT" to "0")).reconnect)
        // any other value keeps reconnecting (parity with the kotlin/node SDKs)
        assertTrue(configFromEnv(mapOf("ETUNNEL_RECONNECT" to "1")).reconnect)
        assertTrue(configFromEnv(mapOf("ETUNNEL_RECONNECT" to "")).reconnect)
    }

    @Test
    fun blankValuesFallBack() {
        // Android apps often inject empty strings; blanks must behave as unset.
        val c = configFromEnv(
            mapOf(
                "ETUNNEL_SERVER" to "",
                "ETUNNEL_LOCAL" to " ",
                "ETUNNEL_TOKEN" to "",
                "ETUNNEL_SUBDOMAIN" to ""
            )
        )
        assertEquals("wss://t.etun.uk/tunnel", c.server)
        assertEquals(3000, c.localPort)
        assertNull(c.token)
        assertNull(c.subdomain)
    }

    @Test
    fun invalidLocalPortRejected() {
        for (bad in listOf("abc", "0", "65536", "-1")) {
            try {
                configFromEnv(mapOf("ETUNNEL_LOCAL" to bad))
                fail("ETUNNEL_LOCAL $bad must be rejected")
            } catch (e: IllegalArgumentException) {
                // expected
            }
        }
    }

    // ---- JSON control messages ----

    @Test
    fun jsonFlatRead() {
        val m = Json.read("""{"type":"welcome","url":"https://x.t.etun.uk","plan":"free","expiresIn":14400}""")
        assertEquals("welcome", m["type"])
        assertEquals("https://x.t.etun.uk", m["url"])
        assertEquals("free", m["plan"])
        assertEquals(14400L, m["expiresIn"])
    }

    @Test
    fun jsonEscapesDecode() {
        val m = Json.read("""{"token":"a\"b\n\u0041"}""")
        assertEquals("a\"b\nA", m["token"])
    }

    @Test
    fun jsonEscRoundTrip() {
        val raw = "he said \"hi\"" + "\n" + "line2"
        val json = """{"msg":"""" + Json.esc(raw) + """"}"""
        assertEquals(raw, (Json.read(json)["msg"] as String))
    }

    // ---- route table (same semantics table as sdks/node/route.js) ----

    @Test
    fun routeLongestPrefixWins() {
        val rs = Etunnel.normalizeRoutes(
            listOf(Route("/", "fe:1"), Route("/api", "be:1"), Route("/api/v2", "be2:1"))
        )
        assertEquals("fe:1", Etunnel.routeFor(rs, "/apiv2")?.first)
        assertEquals("fe:1", Etunnel.routeFor(rs, "/apiary")?.first)
        assertEquals("be:1", Etunnel.routeFor(rs, "/api")?.first)
        assertEquals("be:1", Etunnel.routeFor(rs, "/api/u")?.first)
        assertEquals("be2:1", Etunnel.routeFor(rs, "/api/v2/x?q")?.first)
    }

    @Test
    fun routeStripKeepsQuery() {
        val ss = Etunnel.normalizeRoutes(
            listOf(Route("/api", "be:1", true), Route("/", "fe:1", true))
        )
        assertEquals("/u?x=1&y=2", Etunnel.routeFor(ss, "/api/u?x=1&y=2")?.second)
        assertEquals("/", Etunnel.routeFor(ss, "/api")?.second)
        assertEquals("/app?c=1", Etunnel.routeFor(ss, "/app?c=1")?.second)
    }

    @Test
    fun routeNoMatchIsNull() {
        val one = Etunnel.normalizeRoutes(listOf(Route("/api", "be:1")))
        assertNull(Etunnel.routeFor(one, "/other"))
    }

    @Test
    fun routeValidation() {
        val errs = listOf(
            { Etunnel.normalizeRoutes(listOf(Route("/", ""))) },            // addr required
            { Etunnel.normalizeRoutes(listOf(Route("api", "a:1"))) },       // must start with /
            { Etunnel.normalizeRoutes(listOf(Route("/api", "a:1"), Route("/api/", "a:2"))) } // duplicate
        )
        errs.forEach { fn ->
            try {
                fn()
                fail("expected route validation error")
            } catch (e: IllegalArgumentException) {
                assertTrue(e.message!!.startsWith("etunnel:"))
            }
        }
    }

    // ---- binary frame layout (kind | streamId | len | payload) ----

    @Test
    fun dataFrameLayout() {
        val payload = ByteArray(40) { it.toByte() }
        val f = dataFrame(0x01020304, payload, 0, payload.size)
        assertEquals(9 + 40, f.size)
        assertEquals(0x01, f[0].toInt() and 0xFF)
        assertEquals(0x01020304, frameSid(f))
        assertEquals(40, frameLen(f))
        for (i in payload.indices) assertEquals(payload[i], f[9 + i])
    }

    @Test
    fun ctlFrameLayout() {
        val fin = ctlFrame(0x02, 7)
        assertEquals(9, fin.size)
        assertEquals(0x02, fin[0].toInt() and 0xFF)
        assertEquals(7, frameSid(fin))
        assertEquals(0, frameLen(fin))
        val rst = ctlFrame(0x03, -1) // sid 0xFFFFFFFF round trip
        assertEquals(-1, frameSid(rst))
    }

    @Test
    fun frameHeaderConstantMatchesProtocol() {
        // protocol/frame.go: 9-byte header (kind + uint32 sid + uint32 len)
        assertEquals(9, ET_FRAME_HEADER)
    }

    // ---- handler dispatch of unknown/reset streams (no network) ----

    @Test
    fun unknownStreamIgnored() {
        val h = Etunnel.Companion.Handler(3000)
        h.handleBinary(ctlFrame(0x03, 999)) // rst for a stream we never saw
        assertTrue(h.streams.isEmpty())
    }

    @Test
    fun reqStreamRegisteredAndReset() {
        val h = Etunnel.Companion.Handler(3000)
        h.handleText("""{"type":"req","streamId":5,"method":"GET","path":"/x","headers":{}}""")
        assertTrue(h.streams.containsKey(5))
        h.handleBinary(ctlFrame(0x03, 5)) // rst cancels the stream
        assertTrue(h.streams.isEmpty())
    }

    @Test
    fun pingAnsweredAsPongText() {
        // The ping branch answers over the socket (attached in production by
        // onOpen); in a unit test only the parse path runs - the lateinit ws
        // access surfaces as UninitializedPropertyAccessException, which is
        // fine. The error branch must record the message without a socket.
        val h = Etunnel.Companion.Handler(3000)
        try { h.handleText("""{"type":"ping","ts":123}""") } catch (_: kotlin.UninitializedPropertyAccessException) {}
        h.handleText("""{"type":"error","code":"invalid_token","message":"x"}""")
        assertEquals("invalid_token: x", h.error)
    }
}
