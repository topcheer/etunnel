// etunnel SDK for Kotlin (JDK 11+ http client, zero dependencies).
// Buffer-based protocol client (bodies buffered per stream; no WS upgrades).
//
// Run:   kotlinc Etunnel.kt -include-runtime -d etunnel.jar && java -jar etunnel.jar 3000
// Embed: val t = Etunnel.connect("wss://t.etun.uk/tunnel", 3000)
//        println(t.url)
import java.io.ByteArrayOutputStream
import java.net.URI
import java.net.http.HttpClient
import java.net.http.HttpRequest
import java.net.http.HttpResponse
import java.net.http.WebSocket
import java.nio.ByteBuffer
import java.time.Duration
import java.util.concurrent.CompletionStage
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.CountDownLatch
import java.util.concurrent.atomic.AtomicBoolean

object Json {
    // minimal JSON reader for flat control messages
    fun read(s: String): Map<String, Any?> = parse(s, IntArray(1)) as Map<String, Any?>

    private fun parse(s: String, pos: IntArray): Any? {
        skip(s, pos)
        return when (s[pos[0]]) {
            '{' -> {
                val m = HashMap<String, Any?>()
                pos[0]++
                skip(s, pos)
                if (s[pos[0]] == '}') { pos[0]++; return m }
                while (true) {
                    skip(s, pos)
                    val k = parse(s, pos) as String
                    skip(s, pos); pos[0]++ // ':'
                    m[k] = parse(s, pos)
                    skip(s, pos)
                    if (s[pos[0]] == ',') { pos[0]++; continue }
                    pos[0]++; break
                }
                m
            }
            '[' -> {
                val l = ArrayList<Any?>()
                pos[0]++
                skip(s, pos)
                if (s[pos[0]] == ']') { pos[0]++; return l }
                while (true) {
                    l.add(parse(s, pos))
                    skip(s, pos)
                    if (s[pos[0]] == ',') { pos[0]++; continue }
                    pos[0]++; break
                }
                l
            }
            '"' -> {
                val b = StringBuilder()
                pos[0]++
                while (s[pos[0]] != '"') {
                    if (s[pos[0]] == '\\') {
                        val n = s[pos[0] + 1]
                        when (n) {
                            'n' -> b.append('\n')
                            't' -> b.append('\t')
                            'u' -> { b.append(s.substring(pos[0] + 2, pos[0] + 6).toInt(16).toChar()); pos[0] += 4 }
                            else -> b.append(n)
                        }
                        pos[0] += 2
                    } else { b.append(s[pos[0]]); pos[0]++ }
                }
                pos[0]++
                b.toString()
            }
            else -> {
                val j = pos[0]
                while (pos[0] < s.length && "+-.0123456789eE".indexOf(s[pos[0]]) >= 0) pos[0]++
                val num = s.substring(j, pos[0])
                num.toLongOrNull() ?: num.toDoubleOrNull() ?: num
            }
        }
    }

    private fun skip(s: String, pos: IntArray) {
        while (pos[0] < s.length && s[pos[0]].isWhitespace()) pos[0]++
    }

    fun esc(s: String) = s.replace("\\", "\\\\").replace("\"", "\\\"").replace("\n", "\\n")
}

class Etunnel private constructor(val url: String, val resumeToken: String, private val ws: WebSocket) {
    companion object {
        const val MAXP = 32768

        // Process-level shared clients (issue #51): connect() used to build two
        // fresh HttpClients per call; error paths leaked them (JDK < 21 selector
        // threads are non-daemon). The local client never goes through a system
        // proxy - 127.0.0.1 is always direct (NO_PROXY, macOS global-proxy trap).
        private val LOCAL_HTTP = HttpClient.newBuilder()
            .connectTimeout(Duration.ofSeconds(10))
            .proxy(java.net.ProxySelector.of(null))
            .build()
        private val WS_HTTP = HttpClient.newHttpClient()

        private val HOP = setOf("connection", "transfer-encoding", "keep-alive", "upgrade",
            "proxy-connection", "te", "trailer", "content-length", "host")

        // Handle of the last established socket, so the CLI can watch for a disconnect.
        @Volatile var lastWs: WebSocket? = null

        class Handler(val http: HttpClient, val port: Int) : WebSocket.Listener {
            val welcome = CountDownLatch(1)
            @Volatile var info: Map<String, Any?>? = null
            @Volatile var error: String? = null
            val streams = ConcurrentHashMap<Int, Pair<Map<String, Any?>, ByteArrayOutputStream>>()
            val textBuf = StringBuilder()
            @Volatile lateinit var ws: WebSocket

            override fun onOpen(ws: WebSocket) { ws.request(1) }

            override fun onText(ws: WebSocket, data: CharSequence, last: Boolean): CompletionStage<*>? {
                textBuf.append(data)
                if (last) {
                    val msg = textBuf.toString()
                    textBuf.setLength(0)
                    try { handleText(msg) } catch (_: Exception) {}
                }
                ws.request(1)
                return null
            }

            override fun onBinary(ws: WebSocket, data: ByteBuffer, last: Boolean): CompletionStage<*>? {
                val b = ByteArray(data.remaining())
                data.get(b)
                try { handleBinary(b) } catch (_: Exception) {}
                ws.request(1)
                return null
            }

            override fun onPing(ws: WebSocket, message: ByteBuffer): CompletionStage<*>? {
                ws.sendPong(message)
                ws.request(1)
                return null
            }

            fun handleText(msg: String) {
                val m = Json.read(msg)
                when (m["type"]) {
                    "welcome" -> { info = m; welcome.countDown() }
                    "error" -> { error = "${m["code"]}: ${m["message"] ?: ""}"; welcome.countDown() }
                    "req" -> {
                        val sid = (m["streamId"] as Number).toInt()
                        streams[sid] = m to ByteArrayOutputStream()
                    }
                    "ping" -> ws.sendText("{\"type\":\"pong\",\"ts\":${m["ts"] ?: 0}}", true)
                    "bye" -> ws.sendClose(WebSocket.NORMAL_CLOSURE, "server closed")
                }
            }

            fun handleBinary(b: ByteArray) {
                if (b.size < 9) return
                val kind = b[0].toInt() and 0xFF
                val sid = ((b[1].toInt() and 0xFF) shl 24) or ((b[2].toInt() and 0xFF) shl 16) or
                    ((b[3].toInt() and 0xFF) shl 8) or (b[4].toInt() and 0xFF)
                val st = streams[sid] ?: return
                when (kind) {
                    0x01 -> st.second.write(b, 9, b.size - 9)
                    0x02 -> { streams.remove(sid); serve(st.first, st.second.toByteArray(), sid) }
                    0x03 -> streams.remove(sid)
                }
            }

            @Suppress("UNCHECKED_CAST")
            fun serve(req: Map<String, Any?>, body: ByteArray, sid: Int) {
                try {
                    val method = req["method"] as String
                    val rb = HttpRequest.newBuilder()
                        .uri(URI.create("http://127.0.0.1:$port${req["path"]}"))
                        .timeout(Duration.ofSeconds(120))
                    (req["headers"] as? Map<String, List<String>>)?.forEach { (k, vs) ->
                        val lk = k.lowercase()
                        if (k != null && !HOP.contains(lk)) {
                            vs.forEach { rb.header(k, it) }
                        }
                    }
                    val noBody = method == "GET" || method == "HEAD"
                    rb.method(method,
                        if (noBody) HttpRequest.BodyPublishers.noBody() else HttpRequest.BodyPublishers.ofByteArray(body))
                    val resp: HttpResponse<ByteArray>
                    try {
                        resp = http.send(rb.build(), HttpResponse.BodyHandlers.ofByteArray())
                    } catch (e: Exception) {
                        localError(sid, e.message ?: "error")
                        return
                    }
                    val hb = StringBuilder()
                    resp.headers().map().forEach { (k, vs) ->
                        val lk = k.lowercase()
                        if (!HOP.contains(lk) && lk != "content-length" && lk != "transfer-encoding") {
                            vs.forEach { hb.append(",\"$k\":[\"").append(Json.esc(it)).append("\"]") }
                        }
                    }
                    val hdrJson = if (hb.isEmpty()) "" else hb.substring(1)
                    ws.sendText("{\"type\":\"res\",\"streamId\":$sid,\"status\":${resp.statusCode()},\"headers\":{$hdrJson}}", true).join()
                    val payload = resp.body()
                    var i = 0
                    while (i < payload.size) {
                        val n = minOf(MAXP, payload.size - i)
                        val f = ByteArray(9 + n)
                        f[0] = 0x01
                        putSid(f, sid)
                        putLen(f, n)
                        System.arraycopy(payload, i, f, 9, n)
                        ws.sendBinary(ByteBuffer.wrap(f), true).join()
                        i += n
                    }
                    ctl(0x02, sid)
                } catch (_: Exception) {
                    try { ctl(0x03, sid) } catch (_: Exception) {}
                }
            }

            fun localError(sid: Int, msg: String?) {
                ws.sendText("{\"type\":\"res\",\"streamId\":$sid,\"status\":502,\"headers\":{\"Content-Type\":[\"text/html; charset=utf-8\"]}}", true).join()
                val body = localErrorPage(msg ?: "error").toByteArray()
                val f = ByteArray(9 + body.size)
                f[0] = 0x01
                putSid(f, sid); putLen(f, body.size)
                System.arraycopy(body, 0, f, 9, body.size)
                ws.sendBinary(ByteBuffer.wrap(f), true).join()
                ctl(0x02, sid)
            }

            // Themed 502 page shown when the local app is down (same HTML as the other SDKs).
            fun localErrorPage(msg: String): String {
                val esc = msg.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
                return """<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>502 - local app down</title>
<style>
:root{--bg:#0b0e14;--panel:#0d1117;--line:#21262d;--fg:#e6edf3;--dim:#8b949e;--green:#3fb950}
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:ui-monospace,'SF Mono',Menlo,Consolas,monospace;background:var(--bg);color:var(--fg);
display:flex;min-height:100vh;align-items:center;justify-content:center;line-height:1.6}
main{max-width:600px;padding:2.5rem 1.5rem;width:100%}
.badge{display:inline-block;border:1px solid rgba(248,81,73,.4);color:#f85149;border-radius:99px;
padding:.2rem .8rem;font-size:.72rem;letter-spacing:.08em;text-transform:uppercase;margin-bottom:1.1rem}
h1{font-size:1.6rem;letter-spacing:-.01em;margin-bottom:.4rem}
h1 .g{color:var(--green)}
p{color:var(--dim);font-size:.9rem;margin-bottom:1.4rem}
.panel{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:1.1rem 1.3rem;margin-bottom:1.1rem}
.panel .k{color:var(--dim);font-size:.72rem;text-transform:uppercase;letter-spacing:.08em;margin-bottom:.3rem}
.panel .v{font-size:.9rem;word-break:break-all}
.err{color:#f85149}
.tip{border-left:3px solid var(--green);padding:.8rem 1rem;margin-top:1.2rem;font-size:.82rem;color:var(--dim)}
.tip b{color:var(--fg)}
footer{margin-top:2rem;font-size:.75rem;color:var(--dim)}
footer a{color:#58a6ff;text-decoration:none}
.pulse{display:inline-block;width:.55em;height:.55em;border-radius:50%;background:var(--green);
margin-right:.5em;animation:p 1.2s ease-in-out infinite}
@keyframes p{0%,100%{opacity:1}50%{opacity:.25}}
@media(prefers-reduced-motion:reduce){.pulse{animation:none}}
</style></head><body><main>
<span class="badge">502 local app down</span>
<h1>tunnel up, <span class="g">local app</span> is not</h1>
<p>The tunnel connection is alive, but nothing answered when we knocked.</p>
<div class="panel">
<div class="k">error</div><div class="v err">$esc</div>
</div>
<div class="tip"><span class="pulse"></span><b>Fix:</b> start your local service and reload -
the tunnel stays connected and keeps retrying for you.</div>
<footer>powered by <a href="https://github.com/topcheer/etunnel">etunnel</a></footer>
</main></body></html>"""
            }

            fun ctl(kind: Byte, sid: Int) {
                val f = ByteArray(9)
                f[0] = kind
                putSid(f, sid)
                ws.sendBinary(ByteBuffer.wrap(f), true).join()
            }

            fun putSid(f: ByteArray, sid: Int) {
                f[1] = (sid ushr 24).toByte(); f[2] = (sid ushr 16).toByte()
                f[3] = (sid ushr 8).toByte(); f[4] = sid.toByte()
            }

            fun putLen(f: ByteArray, n: Int) {
                f[5] = (n ushr 24).toByte(); f[6] = (n ushr 16).toByte()
                f[7] = (n ushr 8).toByte(); f[8] = n.toByte()
            }
        }

        fun connect(server: String, localPort: Int, token: String? = null, subdomain: String? = null,
                    resume: String? = null): Etunnel {
            val http = LOCAL_HTTP
            val h = Handler(http, localPort)
            val ws = WS_HTTP
                .newWebSocketBuilder()
                .subprotocols("etunnel-v1")
                .connectTimeout(Duration.ofSeconds(15))
                .buildAsync(URI.create(server), h)
                .join()
            h.ws = ws
            lastWs = ws

            val hello = buildString {
                append("{\"type\":\"hello\",\"proto\":1")
                subdomain?.let { append(",\"subdomain\":\"${Json.esc(it)}\"") }
                token?.let { append(",\"token\":\"${Json.esc(it)}\"") }
                // resume: server-signed token from a previous welcome; the server then
                // hands back the same subdomain instead of allocating a new one.
                resume?.takeIf { it.isNotEmpty() }?.let { append(",\"resume\":\"${Json.esc(it)}\"") }
                append(",\"client\":\"etunnel-kotlin/1.4\"}")
            }
            ws.sendText(hello, true).join()

            // Timed wait (issue #51): a bare await() hung forever if the server
            // completed the handshake but never sent welcome.
            if (!h.welcome.await(15, java.util.concurrent.TimeUnit.SECONDS)) {
                try { ws.sendClose(WebSocket.NORMAL_CLOSURE, "welcome timeout") } catch (_: Exception) {}
                throw RuntimeException("etunnel: no welcome within 15 SECONDS")
            }
            h.error?.let { throw RuntimeException("etunnel: $it") }
            val info = h.info ?: error("no welcome")
            return Etunnel(info["url"] as String, info["resumeToken"] as? String ?: "", ws)
        }
    }

    fun close() {
        // Issue #51: this used to be a no-op - library users had no way to send
        // bye or release the socket. Mirrors the Java Tunnel.close().
        try { ws.sendText("{\"type\":\"bye\",\"reason\":\"client closed\"}", true).join() } catch (_: Exception) {}
        try { ws.sendClose(WebSocket.NORMAL_CLOSURE, "bye") } catch (_: Exception) {}
    }
}

fun main(args: Array<String>) {
    val server = System.getenv("ETUNNEL_SERVER") ?: "wss://t.etun.uk/tunnel"
    val port = (args.getOrNull(0) ?: System.getenv("ETUNNEL_LOCAL") ?: "3000").toInt()
    val token = System.getenv("ETUNNEL_TOKEN")
    val subdomain = System.getenv("ETUNNEL_SUBDOMAIN")

    if (System.getenv("ETUNNEL_RECONNECT") == "0") {
        // original single-shot behavior
        try {
            val t = Etunnel.connect(server, port, token, subdomain)
            println("TUNNEL_URL: ${t.url}")
        } catch (e: Exception) {
            System.err.println("etunnel: ${e.message}")
            kotlin.system.exitProcess(1)
        }
        Thread.currentThread().join()
        return
    }

    val stopped = AtomicBoolean(false)
    Runtime.getRuntime().addShutdownHook(Thread({ stopped.set(true) }, "etunnel-shutdown"))

    var delay = 500L
    var resume = "" // server-signed token: reclaim the same subdomain on reconnect
    while (!stopped.get()) {
        var t: Etunnel? = null
        try {
            t = Etunnel.connect(server, port, token, subdomain, resume.ifEmpty { null })
        } catch (e: Exception) {
            System.err.println("etunnel: ${e.message ?: e.toString()}")
        }
        if (t == null) {
            if (stopped.get()) break
            try { Thread.sleep(jitter(delay)) } catch (ie: InterruptedException) { break }
            delay = nextDelay(delay)
            continue
        }

        delay = 500L // reset after a successful connect
        if (t.resumeToken.isNotEmpty()) resume = t.resumeToken
        println("TUNNEL_URL: ${t.url}")

        val ws = Etunnel.lastWs
        while (!stopped.get() && ws != null && !ws.isInputClosed && !ws.isOutputClosed) {
            try { Thread.sleep(200) } catch (ie: InterruptedException) { break }
        }
        if (stopped.get()) break

        System.err.println("etunnel: disconnected, reconnecting")
        try { Thread.sleep(jitter(delay)) } catch (ie: InterruptedException) { break }
        delay = nextDelay(delay)
    }
}

/** Exponential backoff step, capped at 30s. */
private fun nextDelay(delay: Long): Long = minOf(30000L, delay * 2)

/** +/-20% jitter so many clients don't retry in lockstep. */
private fun jitter(delay: Long): Long = (delay * (0.8 + Math.random() * 0.4)).toLong()
