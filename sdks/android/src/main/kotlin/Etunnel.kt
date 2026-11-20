// etunnel SDK for Android (Kotlin + OkHttp3 WebSocket; no java.net.http -
// that API does not exist on Android). Buffer-based protocol client: bodies
// are buffered per stream, WebSocket (101) pass-through is not forwarded.
//
// Build (AAR + tests):  (cd sdks/android) && ./gradlew build
// Embed (Gradle):       implementation("dev.ggcode:etunnel-android:1.4.0")
// Code:
//   val t = Etunnel.connect("wss://t.etun.uk/tunnel", 3000)
//   Log.i("etunnel", t.url)
import java.io.ByteArrayOutputStream
import java.net.Proxy
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString.Companion.toByteString

/** Local route (PROTOCOL.md §2): path prefix -> local addr ("host:port"). */
data class Route(val prefix: String, val addr: String, val strip: Boolean = false)

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

/** The standard ETUNNEL_* environment contract, parsed from a string map
 *  (the JVM env on desktop, an app-supplied map on Android where env vars
 *  are rarely populated). Mirrors every other SDK's CLI config. */
data class EtunnelConfig(
    val server: String,
    val localPort: Int,
    val token: String?,
    val subdomain: String?,
    val reconnect: Boolean,
)

/** Parse the parity contract (AGENTS.md): ETUNNEL_SERVER / ETUNNEL_LOCAL /
 *  ETUNNEL_TOKEN / ETUNNEL_SUBDOMAIN / ETUNNEL_RECONNECT. Throws
 *  IllegalArgumentException on a non-numeric ETUNNEL_LOCAL. */
fun configFromEnv(env: Map<String, String?>): EtunnelConfig {
    val localRaw = env["ETUNNEL_LOCAL"]?.takeIf { it.isNotBlank() } ?: "3000"
    val port = localRaw.toIntOrNull()
        ?: throw IllegalArgumentException("etunnel: ETUNNEL_LOCAL \"$localRaw\" is not a port number")
    require(port in 1..65535) { "etunnel: ETUNNEL_LOCAL $port out of range 1..65535" }
    return EtunnelConfig(
        server = env["ETUNNEL_SERVER"]?.takeIf { it.isNotBlank() } ?: "wss://t.etun.uk/tunnel",
        localPort = port,
        token = env["ETUNNEL_TOKEN"]?.takeIf { it.isNotBlank() },
        subdomain = env["ETUNNEL_SUBDOMAIN"]?.takeIf { it.isNotBlank() },
        // "0" opts out of the reconnect loop; anything else reconnects (parity).
        reconnect = env["ETUNNEL_RECONNECT"] != "0",
    )
}

// --- wire framing helpers (protocol/protocol frame.go: 9-byte header) ---
internal const val ET_FRAME_HEADER = 9

internal fun putSid(f: ByteArray, sid: Int) {
    f[1] = (sid ushr 24).toByte(); f[2] = (sid ushr 16).toByte()
    f[3] = (sid ushr 8).toByte(); f[4] = sid.toByte()
}

internal fun putLen(f: ByteArray, n: Int) {
    f[5] = (n ushr 24).toByte(); f[6] = (n ushr 16).toByte()
    f[7] = (n ushr 8).toByte(); f[8] = n.toByte()
}

/** kind|streamId|len|payload - kind 0x01 data, 0x02 fin, 0x03 rst. */
internal fun dataFrame(sid: Int, payload: ByteArray, off: Int, len: Int): ByteArray {
    val f = ByteArray(ET_FRAME_HEADER + len)
    f[0] = 0x01
    putSid(f, sid); putLen(f, len)
    System.arraycopy(payload, off, f, ET_FRAME_HEADER, len)
    return f
}

internal fun ctlFrame(kind: Byte, sid: Int): ByteArray {
    val f = ByteArray(ET_FRAME_HEADER)
    f[0] = kind
    putSid(f, sid)
    return f
}

internal class Etunnel(val url: String, val resumeToken: String, private val ws: WebSocket) {
    companion object {
        const val MAXP = 32768

        // Process-level shared clients. LOCAL_HTTP never goes through a system
        // proxy (Proxy.NO_PROXY) - 127.0.0.1 is always direct, and an Android
        // app-level HTTP proxy (VPN/escape hatch apps) must not swallow the
        // tunnel's own upstream requests. callTimeout bounds each local replay.
        private val LOCAL_HTTP = OkHttpClient.Builder()
            .proxy(Proxy.NO_PROXY)
            .connectTimeout(10, TimeUnit.SECONDS)
            .callTimeout(120, TimeUnit.SECONDS)
            .build()
        // WS client: read timeout must stay infinite (the tunnel idles),
        // a 30s WS-level ping keeps NAT/dozed sockets from being dropped
        // silently on mobile networks.
        private val WS_HTTP = OkHttpClient.Builder()
            .connectTimeout(15, TimeUnit.SECONDS)
            .readTimeout(0, TimeUnit.MILLISECONDS)
            .pingInterval(30, TimeUnit.SECONDS)
            .build()

        private val HOP = setOf("connection", "transfer-encoding", "keep-alive", "upgrade",
            "proxy-connection", "te", "trailer", "content-length", "host")

        private val JSON = "application/json; charset=utf-8".toMediaType()

        // Handle of the last established socket's handler, so the CLI loop can
        // watch for a disconnect (OkHttp has no isInputClosed equivalent).
        @Volatile var lastHandler: Handler? = null

        class Handler(
            private val port: Int,
            private val routes: List<Route>? = null,
            private val hello: String = "",
        ) : WebSocketListener() {
            val welcome = CountDownLatch(1)
            @Volatile var info: Map<String, Any?>? = null
            @Volatile var error: String? = null
            @Volatile var open = false
            val streams = ConcurrentHashMap<Int, Pair<Map<String, Any?>, ByteArrayOutputStream>>()
            @Volatile lateinit var ws: WebSocket

            override fun onOpen(ws: WebSocket, response: okhttp3.Response) {
                this.ws = ws
                open = true
                ws.send(hello)
            }

            override fun onMessage(ws: WebSocket, text: String) {
                try { handleText(text) } catch (_: Exception) {}
            }

            override fun onMessage(ws: WebSocket, bytes: okio.ByteString) {
                try { handleBinary(bytes.toByteArray()) } catch (_: Exception) {}
            }

            override fun onClosed(ws: WebSocket, code: Int, reason: String) {
                open = false
                welcome.countDown()
            }

            override fun onFailure(ws: WebSocket, t: Throwable, response: okhttp3.Response?) {
                open = false
                if (error == null) error = t.message ?: "connection failed"
                welcome.countDown()
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
                    "ping" -> ws.send("{\"type\":\"pong\",\"ts\":${m["ts"] ?: 0}}")
                    "bye" -> ws.close(1000, "server closed")
                }
            }

            fun handleBinary(b: ByteArray) {
                if (b.size < ET_FRAME_HEADER) return
                val kind = b[0].toInt() and 0xFF
                val sid = ((b[1].toInt() and 0xFF) shl 24) or ((b[2].toInt() and 0xFF) shl 16) or
                    ((b[3].toInt() and 0xFF) shl 8) or (b[4].toInt() and 0xFF)
                val len = ((b[5].toInt() and 0xFF) shl 24) or ((b[6].toInt() and 0xFF) shl 16) or
                    ((b[7].toInt() and 0xFF) shl 8) or (b[8].toInt() and 0xFF)
                val st = streams[sid] ?: return
                when (kind) {
                    0x01 -> st.second.write(b, ET_FRAME_HEADER, minOf(len, b.size - ET_FRAME_HEADER))
                    0x02 -> { streams.remove(sid); serve(st.first, st.second.toByteArray(), sid) }
                    0x03 -> streams.remove(sid)
                }
            }

            @Suppress("UNCHECKED_CAST")
            fun serve(req: Map<String, Any?>, body: ByteArray, sid: Int) {
                try {
                    val method = req["method"] as String
                    var path = req["path"] as? String ?: "/"
                    var target = "http://127.0.0.1:$port"
                    if (routes != null) {
                        val hit = routeFor(routes, path) ?: run { notFound(sid); return }
                        target = "http://${hit.first}"
                        path = hit.second
                    }
                    val rb = Request.Builder().url(target + path)
                    (req["headers"] as? Map<String, List<String>>)?.forEach { (k, vs) ->
                        val lk = k.lowercase()
                        if (lk != null && !HOP.contains(lk)) {
                            vs.forEach { rb.header(k, it) }
                        }
                    }
                    val noBody = method == "GET" || method == "HEAD"
                    rb.method(method,
                        if (noBody) null else body.toRequestBody(JSON))
                    val resp: okhttp3.Response
                    try {
                        resp = LOCAL_HTTP.newCall(rb.build()).execute()
                    } catch (e: Exception) {
                        localError(sid, e.message ?: "error")
                        return
                    }
                    resp.use {
                        val hb = StringBuilder()
                        it.headers.forEach { (k, v) ->
                            val lk = k.lowercase()
                            if (!HOP.contains(lk) && lk != "content-length" && lk != "transfer-encoding") {
                                hb.append(",\"$k\":[\"").append(Json.esc(v)).append("\"]")
                            }
                        }
                        val hdrJson = if (hb.isEmpty()) "" else hb.substring(1)
                        ws.send("{\"type\":\"res\",\"streamId\":$sid,\"status\":${it.code},\"headers\":{$hdrJson}}")
                        val payload = it.body?.bytes() ?: ByteArray(0)
                        var i = 0
                        while (i < payload.size) {
                            val n = minOf(MAXP, payload.size - i)
                            ws.send(dataFrame(sid, payload, i, n).toByteString())
                            i += n
                        }
                        ws.send(ctlFrame(0x02, sid).toByteString())
                    }
                } catch (_: Exception) {
                    try { ws.send(ctlFrame(0x03, sid).toByteString()) } catch (_: Exception) {}
                }
            }

            fun localError(sid: Int, msg: String?) {
                ws.send("{\"type\":\"res\",\"streamId\":$sid,\"status\":502,\"headers\":{\"Content-Type\":[\"text/html; charset=utf-8\"]}}")
                val body = localErrorPage(msg ?: "error").toByteArray()
                ws.send(dataFrame(sid, body, 0, body.size).toByteString())
                ws.send(ctlFrame(0x02, sid).toByteString())
            }

            /** No route matched: answer 404 at the SDK edge (PROTOCOL.md §2). */
            fun notFound(sid: Int) {
                ws.send("{\"type\":\"res\",\"streamId\":$sid,\"status\":404,\"headers\":{\"Content-Type\":[\"text/plain; charset=utf-8\"]}}")
                val body = "404 page not found (no route matches this path)\n".toByteArray()
                ws.send(dataFrame(sid, body, 0, body.size).toByteString())
                ws.send(ctlFrame(0x02, sid).toByteString())
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

            fun ctl(kind: Byte, sid: Int) { ws.send(ctlFrame(kind, sid).toByteString()) }
        }

        /** Validate + compile a route table: trims trailing slashes ("/" stays),
         *  rejects duplicates and malformed prefixes, sorts longest-first. */
        fun normalizeRoutes(routes: List<Route>): List<Route> {
            val out = ArrayList<Route>()
            val seen = HashSet<String>()
            routes.forEachIndexed { i, r ->
                require(r.prefix.isNotEmpty()) { "etunnel: route[$i]: prefix is required (use \"/\" for the fallback route)" }
                require(r.addr.isNotEmpty()) { "etunnel: route[$i]: addr is required" }
                require(r.prefix.startsWith("/")) { "etunnel: route[$i]: prefix \"${r.prefix}\" must start with '/'" }
                var p = r.prefix
                while (p.length > 1 && p.endsWith("/")) p = p.dropLast(1)
                require(seen.add(p)) { "etunnel: route[$i]: duplicate prefix \"$p\"" }
                out.add(Route(p, r.addr, r.strip))
            }
            return out.sortedByDescending { it.prefix.length }
        }

        /** Resolve "path?query" (segment boundaries; strip keeps the query
         *  byte-for-byte). null when nothing matches. */
        fun routeFor(routes: List<Route>, pathAndQuery: String): Pair<String, String>? {
            val qi = pathAndQuery.indexOf('?')
            val path = if (qi >= 0) pathAndQuery.substring(0, qi) else pathAndQuery
            val query = if (qi >= 0) pathAndQuery.substring(qi) else ""
            for (r in routes) {
                if (!(r.prefix == "/" || path == r.prefix || path.startsWith(r.prefix + "/"))) continue
                var p = path
                if (r.strip && r.prefix != "/") p = if (path == r.prefix) "/" else path.substring(r.prefix.length)
                return r.addr to p + query
            }
            return null
        }

        fun connect(server: String, localPort: Int, token: String? = null, subdomain: String? = null,
                    resume: String? = null, routes: List<Route>? = null): Etunnel {
            val norm = routes?.let { normalizeRoutes(it) }
            if (norm != null && norm.isEmpty()) throw IllegalArgumentException("etunnel: routes must not be empty")
            val hello = buildString {
                append("{\"type\":\"hello\",\"proto\":1")
                subdomain?.let { append(",\"subdomain\":\"${Json.esc(it)}\"") }
                token?.let { append(",\"token\":\"${Json.esc(it)}\"") }
                // resume: server-signed token from a previous welcome; the server then
                // hands back the same subdomain instead of allocating a new one.
                resume?.takeIf { it.isNotEmpty() }?.let { append(",\"resume\":\"${Json.esc(it)}\"") }
                append(",\"client\":\"etunnel-android/1.4\"}")
            }
            val h = Handler(localPort, norm, hello)
            // Subprotocol selection: OkHttp has no builder API for it, but it
            // honours a request Sec-WebSocket-Protocol header (and rejects the
            // handshake if the server picks an unrequested subprotocol).
            val ws = WS_HTTP.newWebSocket(
                Request.Builder().url(server).header("Sec-WebSocket-Protocol", "etunnel-v1").build(),
                h)

            // Timed wait (issue #51 parity): a bare await() hung forever if the
            // server completed the handshake but never sent welcome.
            if (!h.welcome.await(15, TimeUnit.SECONDS)) {
                try { ws.close(1000, "welcome timeout") } catch (_: Exception) {}
                throw RuntimeException("etunnel: no welcome within 15 SECONDS")
            }
            h.error?.let {
                try { ws.close(1000, "connect failed") } catch (_: Exception) {}
                throw RuntimeException("etunnel: $it")
            }
            val info = h.info ?: error("no welcome")
            lastHandler = h
            return Etunnel(info["url"] as String, info["resumeToken"] as? String ?: "", ws)
        }
    }

    fun close() {
        // Library users need a way to send bye and release the socket
        // (mirrors the Kotlin SDK's Tunnel.close()).
        try { ws.send("{\"type\":\"bye\",\"reason\":\"client closed\"}") } catch (_: Exception) {}
        try { ws.close(1000, "bye") } catch (_: Exception) {}
    }
}

fun main(args: Array<String>) {
    // CLI parity: honours the standard ETUNNEL_* environment variables and
    // prints TUNNEL_URL: <url> on connect (AGENTS.md cross-SDK contract).
    val cfg = configFromEnv(System.getenv())
    if (!cfg.reconnect) {
        // original single-shot behavior
        try {
            val t = Etunnel.connect(cfg.server, cfg.localPort, cfg.token, cfg.subdomain)
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
            t = Etunnel.connect(cfg.server, cfg.localPort, cfg.token, cfg.subdomain, resume.ifEmpty { null })
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

        val h = Etunnel.lastHandler
        while (!stopped.get() && h != null && h.open) {
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
