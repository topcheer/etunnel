// Pure-JVM unit tests for the etunnel Android SDK: no Android runtime, no
// network dependency beyond loopback sockets. Covers the cross-SDK parity
// seams (ETUNNEL_* env mapping, JSON, routes, 9-byte frames) and the #282
// review fixes: #1 replay off the WS reader thread, #2 multi-value headers,
// #3 M2 caps with RST.
import java.net.InetAddress
import java.net.ServerSocket
import java.net.Socket
import java.net.Proxy
import java.util.concurrent.TimeUnit
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.WebSocket
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
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

/** Test double for the OkHttp WebSocket: records sends/closes. */
private class RecordingWebSocket : WebSocket {
    val texts = ArrayList<String>()
    val bin = ArrayList<ByteArray>()
    @Volatile var closed = false
    @Volatile var closeCode = 0
    override fun request(): Request = Request.Builder().url("http://127.0.0.1/").build()
    override fun queueSize(): Long = 0
    override fun send(text: String): Boolean { synchronized(texts) { texts.add(text) }; return true }
    override fun send(bytes: okio.ByteString): Boolean { synchronized(bin) { bin.add(bytes.toByteArray()) }; return true }
    override fun close(code: Int, reason: String?): Boolean { closed = true; closeCode = code; return true }
    override fun cancel() {}
}

/** Loopback server factory: each connection handled on its own daemon thread. */
private fun tinyHttpServer(responder: (Socket) -> Unit): ServerSocket {
    val ss = ServerSocket(0, 64, InetAddress.getLoopbackAddress())
    val acceptor = Thread {
        while (!ss.isClosed) {
            val s = try { ss.accept() } catch (e: Exception) { return@Thread }
            Thread { responder(s) }.apply { isDaemon = true }.start()
        }
    }
    acceptor.isDaemon = true
    acceptor.start()
    return ss
}

private fun awaitUntil(timeoutMs: Long, cond: () -> Boolean): Boolean {
    val deadline = System.currentTimeMillis() + timeoutMs
    while (System.currentTimeMillis() < deadline) {
        if (cond()) return true
        Thread.sleep(20)
    }
    return cond()
}

private fun rstSids(ws: RecordingWebSocket): List<Int> = synchronized(ws.bin) {
    ws.bin.filter { it.size == 9 && (it[0].toInt() and 0xFF) == 0x03 }.map { frameSid(it) }
}

class EtunnelSdkTest {

    // ---- ETUNNEL_* env contract (configFromEnv) ----

    @Test
    fun envDefaults() {
        val c = configFromEnv(emptyMap())
        assertEquals("wss://t.etun.uk/tunnel", c.server)
        assertEquals("3000", c.local)
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
        assertEquals("8080", c.local)
        assertEquals("tok123", c.token)
        assertEquals("my-sub", c.subdomain)
        assertTrue(c.reconnect)
    }

    @Test
    fun hostPortLocalAccepted() {
        // #282-5: "host:port" is contract-legal, not an error.
        assertEquals("localhost:8080", configFromEnv(mapOf("ETUNNEL_LOCAL" to "localhost:8080")).local)
        assertEquals("myhost", configFromEnv(mapOf("ETUNNEL_LOCAL" to "myhost")).local)
        assertEquals("[::1]:9000", configFromEnv(mapOf("ETUNNEL_LOCAL" to "[::1]:9000")).local)
    }

    @Test
    fun reconnectZeroDisables() {
        assertTrue(!configFromEnv(mapOf("ETUNNEL_RECONNECT" to "0")).reconnect)
        assertTrue(configFromEnv(mapOf("ETUNNEL_RECONNECT" to "1")).reconnect)
        assertTrue(configFromEnv(mapOf("ETUNNEL_RECONNECT" to "")).reconnect)
    }

    @Test
    fun blankValuesFallBack() {
        val c = configFromEnv(
            mapOf(
                "ETUNNEL_SERVER" to "",
                "ETUNNEL_LOCAL" to " ",
                "ETUNNEL_TOKEN" to "",
                "ETUNNEL_SUBDOMAIN" to ""
            )
        )
        assertEquals("wss://t.etun.uk/tunnel", c.server)
        assertEquals("3000", c.local)
        assertNull(c.token)
        assertNull(c.subdomain)
    }

    @Test
    fun invalidLocalRejected() {
        // #282-5: bare "-1" is a bare-host form now (port 80, TS parity) - valid
        for (bad in listOf("0", "65536", "abc:70000", "abc:notaport")) {
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
    fun jsonLiterals() {
        // #282-7: null/true/false are parsed, not turned into ""
        val m = Json.read("""{"a":null,"b":true,"c":false}""")
        assertTrue(m.containsKey("a"))
        assertNull(m["a"])
        assertEquals(true, m["b"])
        assertEquals(false, m["c"])
    }

    @Test
    fun jsonGarbageDoesNotStall() {
        // unknown tokens skip one char - parsing must always terminate
        val m = Json.read("""{"a":%zz}""")
        assertEquals("", m["a"])
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

    @Test
    fun jsonEscControlChars() {
        // #282-6: raw control characters must be escaped, not emitted verbatim
        assertEquals("a\\tb\\r\\nc\\u0001", Json.esc("a\tb\r\nc\u0001"))
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
            { Etunnel.normalizeRoutes(listOf(Route("/", ""))) },
            { Etunnel.normalizeRoutes(listOf(Route("api", "a:1"))) },
            { Etunnel.normalizeRoutes(listOf(Route("/api", "a:1"), Route("/api/", "a:2"))) }
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
        val rst = ctlFrame(0x03, -1)
        assertEquals(-1, frameSid(rst))
    }

    @Test
    fun frameHeaderConstantMatchesProtocol() {
        assertEquals(9, ET_FRAME_HEADER)
    }

    // ---- host:port resolution (#282-5) ----

    @Test
    fun pickHostPortForms() {
        assertEquals("127.0.0.1" to 3000, Etunnel.pickHostPort("3000"))
        assertEquals("localhost" to 8080, Etunnel.pickHostPort("localhost:8080"))
        assertEquals("[::1]" to 9000, Etunnel.pickHostPort("[::1]:9000"))
        assertEquals("example.com" to 80, Etunnel.pickHostPort("example.com"))
        assertEquals("[2001:db8::1]" to 80, Etunnel.pickHostPort("2001:db8::1"))
    }

    // ---- handler dispatch (no network) ----

    private fun handlerWithWs(local: String = "3000", maxStreams: Int = Etunnel.MAX_STREAMS,
                              maxStreamBody: Int = Etunnel.MAX_STREAM_BODY): Pair<Etunnel.Companion.Handler, RecordingWebSocket> {
        val h = Etunnel.Companion.Handler(local, maxStreams = maxStreams, maxStreamBody = maxStreamBody)
        val ws = RecordingWebSocket()
        h.ws = ws
        h.open = true
        return h to ws
    }

    @Test
    fun unknownStreamIgnored() {
        val (h, ws) = handlerWithWs()
        h.handleBinary(ctlFrame(0x03, 999))
        assertTrue(ws.bin.isEmpty())
        assertTrue(h.streams.isEmpty())
    }

    @Test
    fun reqStreamRegisteredAndReset() {
        val (h, ws) = handlerWithWs()
        h.handleText("""{"type":"req","streamId":5,"method":"GET","path":"/x","headers":{}}""")
        assertTrue(h.streams.containsKey(5))
        h.handleBinary(ctlFrame(0x03, 5))
        assertTrue(h.streams.isEmpty())
        assertTrue(ws.bin.isEmpty())
    }

    @Test
    fun pingTsGuard() {
        // #282-7: only numeric ts reaches the wire; literals/absence -> 0
        val (h, ws) = handlerWithWs()
        h.handleText("""{"type":"ping","ts":123}""")
        h.handleText("""{"type":"ping","ts":null}""")
        h.handleText("""{"type":"ping"}""")
        synchronized(ws.texts) {
            assertEquals(listOf("123", "0", "0"), ws.texts.map { it.substringAfter("\"ts\":").removeSuffix("}") })
        }
    }

    @Test
    fun oversizeMessageDropsSocket() {
        // #282-3: message over MAX_WS_MSG -> socket dropped with 1009
        val (h, ws) = handlerWithWs()
        h.handleText("x".repeat(Etunnel.MAX_WS_MSG + 1))
        assertTrue(ws.closed)
        assertEquals(1009, ws.closeCode)
        assertFalse(h.open)
    }

    @Test
    fun bodyOverCapGetsRst() {
        // #282-3: buffered body over the cap -> RST, stream dropped
        val (h, ws) = handlerWithWs(maxStreamBody = 64)
        h.handleText("""{"type":"req","streamId":1,"method":"POST","path":"/","headers":{}}""")
        h.handleBinary(dataFrame(1, ByteArray(100), 0, 100))
        assertEquals(listOf(1), rstSids(ws))
        assertFalse(h.streams.containsKey(1))
    }

    @Test
    fun onLogHookReceivesErrors() {
        // #282-8: swallowed errors are surfaced through the log hook
        val logs = ArrayList<String>()
        Etunnel.onLog = { logs.add(it) }
        try {
            val (h, _) = handlerWithWs()
            h.handleText("not json")
            assertTrue(awaitUntil(2000) { logs.any { it.contains("handleText") } })
        } finally {
            Etunnel.onLog = null
        }
    }

    // ---- #282-3: stream caps refuse with RST ----

    @Test
    fun overCapStreamGetsRst() {
        val (h, ws) = handlerWithWs(maxStreams = 2)
        h.handleText("""{"type":"req","streamId":1,"method":"GET","path":"/","headers":{}}""")
        h.handleText("""{"type":"req","streamId":2,"method":"GET","path":"/","headers":{}}""")
        // replayed live streamId -> RST instead of silent overwrite
        h.handleText("""{"type":"req","streamId":1,"method":"GET","path":"/","headers":{}}""")
        // beyond the cap -> RST
        h.handleText("""{"type":"req","streamId":3,"method":"GET","path":"/","headers":{}}""")
        assertEquals(listOf(1, 3), rstSids(ws))
        assertEquals(2, h.streams.size)
    }

    // ---- #282-1: replay must not run on the caller (WS reader) thread ----

    @Test
    fun slowReplayDoesNotBlockReader() {
        // local server accepts but never answers: replay stalls until the
        // injected client's callTimeout (500ms) fires
        val ss = tinyHttpServer { s -> Thread.sleep(5000); s.close() }
        try {
            val slowClient = OkHttpClient.Builder()
                .proxy(Proxy.NO_PROXY)
                .connectTimeout(2, TimeUnit.SECONDS)
                .callTimeout(500, TimeUnit.MILLISECONDS)
                .build()
            val h = Etunnel.Companion.Handler(ss.localPort.toString(), client = slowClient)
            val ws = RecordingWebSocket()
            h.ws = ws
            h.open = true
            h.handleText("""{"type":"req","streamId":1,"method":"GET","path":"/","headers":{}}""")

            val t0 = System.currentTimeMillis()
            h.handleBinary(ctlFrame(0x02, 1)) // FIN: schedules the replay
            val elapsed = System.currentTimeMillis() - t0
            assertTrue("handleBinary blocked ${elapsed}ms on the replay - reader thread starves (#282-1)", elapsed < 200)

            // the offloaded replay eventually completes and answers 502
            assertTrue(awaitUntil(5000) {
                synchronized(ws.texts) { ws.texts.any { it.contains("\"status\":502") } }
            })
            // and the slow replay must NOT have torn the tunnel down
            assertFalse("slow replay must not kill the tunnel (#282-1)", ws.closed)
            assertTrue(h.open)
        } finally {
            ss.close()
        }
    }

    // ---- #282-2: multi-value request headers preserved ----

    @Test
    fun multiValueRequestHeadersPreserved() {
        val seen = java.util.Collections.synchronizedList(ArrayList<String>())
        val ss = tinyHttpServer { s ->
            try {
                val reader = s.getInputStream().bufferedReader()
                val lines = ArrayList<String>()
                var line = reader.readLine()
                while (line != null && line.isNotEmpty()) {
                    lines.add(line)
                    line = reader.readLine()
                }
                seen.addAll(lines.filter { it.startsWith("X-Multi:") })
                s.getOutputStream().write("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok".toByteArray())
                s.close()
            } catch (_: Exception) { /* test teardown races are fine */ }
        }
        try {
            val h = Etunnel.Companion.Handler(ss.localPort.toString())
            val ws = RecordingWebSocket()
            h.ws = ws
            h.open = true
            h.handleText("""{"type":"req","streamId":7,"method":"POST","path":"/x","headers":{"X-Multi":["a","b"],"X-Once":["z"]}}""")
            h.handleBinary(ctlFrame(0x02, 7))
            assertTrue(awaitUntil(5000) {
                synchronized(ws.texts) { ws.texts.any { it.contains("\"status\":200") } }
            })
            assertEquals(listOf("X-Multi: a", "X-Multi: b"), seen.toList())
        } finally {
            ss.close()
        }
    }

    // ---- #286: gzip+chunked replay stays identity on the tunnel wire ----

    @Test
    fun gzipChunkedReplayStaysIdentity() {
        // Local backend answers like NanoHTTPD does for gzip-negotiating
        // visitors: Content-Encoding: gzip, chunked transfer framing, no
        // Content-Length. The bytes replayed onto the tunnel wire must be
        // the pure gzip entity - no chunk-size lines in front of it, and no
        // Transfer-Encoding / stale Content-Length headers crossing the
        // wire (PROTOCOL.md §2 identity-body invariant, #286).
        val original = "etunnel #286 identity-body payload - ".repeat(42).toByteArray()
        val gzBuf = java.io.ByteArrayOutputStream()
        java.util.zip.GZIPOutputStream(gzBuf).use { it.write(original) }
        val gz = gzBuf.toByteArray()

        val ss = tinyHttpServer { s ->
            try {
                val reader = s.getInputStream().bufferedReader()
                while (reader.readLine()?.isNotEmpty() == true) { /* drain the request head */ }
                val out = s.getOutputStream()
                // Hand-rolled chunked framing: two chunks plus last-chunk,
                // exactly what the local service put on its wire.
                val a = gz.copyOfRange(0, gz.size / 2)
                val b = gz.copyOfRange(gz.size / 2, gz.size)
                out.write(("HTTP/1.1 200 OK\r\nContent-Encoding: gzip\r\nContent-Type: text/html; charset=utf-8\r\nTransfer-Encoding: chunked\r\n\r\n" +
                    a.size.toString(16) + "\r\n").toByteArray())
                out.write(a)
                out.write("\r\n".toByteArray())
                out.write((b.size.toString(16) + "\r\n").toByteArray())
                out.write(b)
                out.write("\r\n".toByteArray())
                out.write("0\r\n\r\n".toByteArray())
                out.flush()
                s.close()
            } catch (_: Exception) { /* test teardown races are fine */ }
        }
        try {
            val h = Etunnel.Companion.Handler(ss.localPort.toString())
            val ws = RecordingWebSocket()
            h.ws = ws
            h.open = true
            h.handleText("""{"type":"req","streamId":9,"method":"GET","path":"/index.html","headers":{"Accept-Encoding":["gzip"]}}""")
            h.handleBinary(ctlFrame(0x02, 9)) // FIN: schedules the replay
            assertTrue(awaitUntil(5000) {
                synchronized(ws.bin) { ws.bin.any { it.size == 9 && (it[0].toInt() and 0xFF) == 0x02 && frameSid(it) == 9 } }
            })

            val resJson: String = synchronized(ws.texts) { ws.texts.firstOrNull { it.contains("\"type\":\"res\"") } }
                ?: throw AssertionError("no res sent")
            assertTrue("Content-Encoding must survive the replay", resJson.contains("\"Content-Encoding\""))
            assertFalse("Transfer-Encoding must never cross the wire (#286)", resJson.contains("Transfer-Encoding"))
            assertFalse("Content-Length must never cross the wire (#286)", resJson.contains("Content-Length"))

            val body = synchronized(ws.bin) {
                ws.bin.filter { it.size > 9 && (it[0].toInt() and 0xFF) == 0x01 && frameSid(it) == 9 }
                    .flatMap { f -> f.toList().drop(9) }
                    .toByteArray()
            }
            assertArrayEquals("replayed body must be the pure gzip entity, framing stripped (#286)", gz, body)
        } finally {
            ss.close()
        }
    }

    @Test
    fun rawChunkedReplayStaysIdentity() {
        // #286 blind-spot hardening: same identity invariant as
        // gzipChunkedReplayStaysIdentity, but the local backend is a raw
        // byte-for-byte responder - it never reads the request head and
        // writes ONE prebuilt wire buffer (three chunks, multi-digit hex
        // sizes, last-chunk octets). A raw parser/replayer that copied the
        // chunked framing verbatim onto DATA would fail the byte-identity
        // assertion below: chunk-size lines and the "0\r\n\r\n" terminator
        // would sit inside the replayed body (PROTOCOL.md §2).
        val original = "etunnel #286 raw-socket identity payload - ".repeat(37).toByteArray()
        val gzBuf = java.io.ByteArrayOutputStream()
        java.util.zip.GZIPOutputStream(gzBuf).use { it.write(original) }
        val gz = gzBuf.toByteArray()

        val parts = listOf(
            gz.copyOfRange(0, gz.size / 3),
            gz.copyOfRange(gz.size / 3, 2 * gz.size / 3),
            gz.copyOfRange(2 * gz.size / 3, gz.size),
        )
        val rawWire = java.io.ByteArrayOutputStream().also { buf ->
            buf.write("HTTP/1.1 200 OK\r\nContent-Encoding: gzip\r\nContent-Type: text/html; charset=utf-8\r\nTransfer-Encoding: chunked\r\n\r\n".toByteArray())
            for (p in parts) {
                buf.write((p.size.toString(16) + "\r\n").toByteArray())
                buf.write(p)
                buf.write("\r\n".toByteArray())
            }
            buf.write("0\r\n\r\n".toByteArray())
        }.toByteArray()

        val ss = ServerSocket(0, 64, InetAddress.getLoopbackAddress())
        val acceptor = Thread {
            val s = try { ss.accept() } catch (_: Exception) { return@Thread }
            try {
                s.getOutputStream().write(rawWire) // raw: the request is never read
                s.getOutputStream().flush()
            } catch (_: Exception) { /* test teardown races are fine */ }
        }
        acceptor.isDaemon = true
        acceptor.start()
        try {
            val h = Etunnel.Companion.Handler(ss.localPort.toString())
            val ws = RecordingWebSocket()
            h.ws = ws
            h.open = true
            h.handleText("""{"type":"req","streamId":11,"method":"GET","path":"/raw","headers":{"Accept-Encoding":["gzip"]}}""")
            h.handleBinary(ctlFrame(0x02, 11)) // FIN: schedules the replay
            assertTrue(awaitUntil(5000) {
                synchronized(ws.bin) { ws.bin.any { it.size == 9 && (it[0].toInt() and 0xFF) == 0x02 && frameSid(it) == 11 } }
            })

            val resJson: String = synchronized(ws.texts) { ws.texts.firstOrNull { it.contains("\"type\":\"res\"") } }
                ?: throw AssertionError("no res sent")
            assertTrue(resJson.contains("\"status\":200"))
            assertFalse("Transfer-Encoding must never cross the wire (#286)", resJson.contains("Transfer-Encoding"))
            assertFalse("Content-Length must never cross the wire (#286)", resJson.contains("Content-Length"))

            val body = synchronized(ws.bin) {
                ws.bin.filter { it.size > 9 && (it[0].toInt() and 0xFF) == 0x01 && frameSid(it) == 11 }
                    .flatMap { f -> f.toList().drop(9) }
                    .toByteArray()
            }
            assertArrayEquals("raw chunked backend: replayed DATA must be the pure gzip entity (#286)", gz, body)
            // belt-and-braces: chunk framing artifacts would break byte identity
            assertFalse(String(body, Charsets.ISO_8859_1).contains("\r\n0\r\n"))
        } finally {
            ss.close()
        }
    }
}
