// etunnel SDK for Android (Kotlin + OkHttp3 WebSocket; no java.net.http -
// that API does not exist on Android). Buffer-based protocol client: bodies
// are buffered per stream, WebSocket (101) pass-through is not forwarded.
//
// Build (AAR + tests):  (cd sdks/android) && ./gradlew build
// Embed (Gradle):       implementation("dev.ggcode:etunnel-android:<version>")
// Code:
//   val t = Etunnel.connect("wss://t.etun.uk/tunnel", 3000)
//   Log.i("etunnel", t.url)
//
// Every declaration lives in the package the AAR publishes under (the
// namespace in build.gradle.kts only names the R class - Kotlin classes
// without a package declaration land in the root package, so the README's
// `import dev.ggcode.etunnel.Etunnel` could never resolve).
package dev.ggcode.etunnel

import java.io.ByteArrayOutputStream
import java.net.Proxy
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.CountDownLatch
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors
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
                when {
                    // #282-7: bare JSON literals parsed as-is, not empty strings
                    s.startsWith("null", j) -> { pos[0] = j + 4; null }
                    s.startsWith("true", j) -> { pos[0] = j + 4; true }
                    s.startsWith("false", j) -> { pos[0] = j + 5; false }
                    else -> {
                        while (pos[0] < s.length && "+-.0123456789eE".indexOf(s[pos[0]]) >= 0) pos[0]++
                        val num = s.substring(j, pos[0])
                        if (num.isEmpty()) {
                            // unknown token: skip one char so parsing can never stall
                            pos[0] = j + 1
                            ""
                        } else num.toLongOrNull() ?: num.toDoubleOrNull() ?: num
                    }
                }
            }
        }
    }

    private fun skip(s: String, pos: IntArray) {
        while (pos[0] < s.length && s[pos[0]].isWhitespace()) pos[0]++
    }

    // #282-6: escape every control character - a raw \t (or any char < 0x20)
    // inside a value produced invalid JSON and the server tore the session.
    fun esc(s: String): String {
        val b = StringBuilder(s.length + 8)
        for (c in s) {
            when {
                c == '\\' -> b.append("\\\\")
                c == '"' -> b.append("\\\"")
                c == '\n' -> b.append("\\n")
                c == '\r' -> b.append("\\r")
                c == '\t' -> b.append("\\t")
                c < ' ' -> b.append("\\u%04x".format(c.code))
                else -> b.append(c)
            }
        }
        return b.toString()
    }
}

/** The standard ETUNNEL_* environment contract, parsed from a string map
 *  (the JVM env on desktop, an app-supplied map on Android where env vars
 *  are rarely populated). Mirrors every other SDK's CLI config. */
data class EtunnelConfig(
    val server: String,
    /** Port number or "host:port" (contract allows both, AGENTS.md). */
    val local: String,
    val token: String?,
    val subdomain: String?,
    val reconnect: Boolean,
)

/** "3000", "host:port", "[v6]:port" or a bare host (port 80, TS parity). */
internal fun validateLocal(local: String) {
    if (local.matches(Regex("\\d+"))) {
        val port = local.toInt()
        require(port in 1..65535) { "etunnel: ETUNNEL_LOCAL $port out of range 1..65535" }
        return
    }
    if (local.contains(':')) {
        // any colon form must be a well-formed host:port with a numeric port
        val m = Regex("^(\\[[^]]+\\]|[^:]+):(\\d+)$").find(local)
            ?: throw IllegalArgumentException("etunnel: ETUNNEL_LOCAL \"$local\" is not host:port")
        val port = m.groupValues[2].toInt()
        require(port in 1..65535) { "etunnel: ETUNNEL_LOCAL \"$local\" port out of range 1..65535" }
    }
    // bare host (no colon): valid, port defaults to 80 (TS parity)
}

/** Parse the parity contract (AGENTS.md): ETUNNEL_SERVER / ETUNNEL_LOCAL /
 *  ETUNNEL_TOKEN / ETUNNEL_SUBDOMAIN / ETUNNEL_RECONNECT. Throws
 *  IllegalArgumentException on a malformed ETUNNEL_LOCAL (#282-5: "host:port"
 *  is contract-legal, not an error). */
fun configFromEnv(env: Map<String, String?>): EtunnelConfig {
    val localRaw = env["ETUNNEL_LOCAL"]?.takeIf { it.isNotBlank() } ?: "3000"
    validateLocal(localRaw)
    return EtunnelConfig(
        server = env["ETUNNEL_SERVER"]?.takeIf { it.isNotBlank() } ?: "wss://t.etun.uk/tunnel",
        local = localRaw,
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

// Public API surface (README "Use"): connect()/url/resumeToken/close(). The
// constructor stays internal - clients obtain an instance only through
// connect(), which has already completed the hello/welcome handshake.
class Etunnel internal constructor(val url: String, val resumeToken: String, private val ws: WebSocket) {
    companion object {
        const val MAXP = 32768
        /** SDK version reported in hello.version; stamped by scripts/bump-release.py. */
        const val VERSION = "1.7.50"

        // #282-3: #243 M2 resource caps (sdk/stream.go parity). A hostile or
        // buggy server must not be able to OOM the client.
        internal const val MAX_WS_MSG = 128 * 1024
        internal const val MAX_STREAMS = 256
        internal const val MAX_STREAM_BODY = 16 * 1024 * 1024

        // #282-8: optional sink for otherwise-swallowed errors (no logging
        // framework, Go SDK OnLog parity). Null by default: fully silent.
        @Volatile var onLog: ((String) -> Unit)? = null
        internal fun logMsg(msg: String) { onLog?.invoke("etunnel: $msg") }
        internal fun logError(where: String, e: Exception) {
            onLog?.invoke("etunnel: $where: ${e.message ?: e.toString()}")
        }

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
            local: String,
            private val routes: List<Route>? = null,
            private val hello: String = "",
            // Injectable so unit tests can bound replay latency (#282 test 1).
            private val client: OkHttpClient = LOCAL_HTTP,
            // #282-3: caps are parameters so unit tests can shrink them;
            // production defaults are the #243 M2 values.
            internal val maxStreams: Int = MAX_STREAMS,
            internal val maxStreamBody: Int = MAX_STREAM_BODY,
        ) : WebSocketListener() {
            private val hostPort: Pair<String, Int> = pickHostPort(local)
            // #282-1: dedicated single-thread replay executor - ordering kept,
            // the WS reader thread only enqueues; daemon so it never blocks exit.
            private val replayPool: ExecutorService = Executors.newSingleThreadExecutor { r ->
                Thread(r, "etunnel-replay").apply { isDaemon = true }
            }
            val welcome = CountDownLatch(1)
            @Volatile var info: Map<String, Any?>? = null
            @Volatile var error: String? = null
            @Volatile var open = false
            val streams = ConcurrentHashMap<Int, Pair<Map<String, Any?>, ByteArrayOutputStream>>()
            @Volatile lateinit var ws: WebSocket

            /** #282-3: protocol-violation path - drop the socket (code 1009)
             *  so the supervisor reconnects instead of limping on poisoned state. */
            fun failSocket(reason: String) {
                open = false
                logMsg("socket dropped: $reason")
                try { ws.close(1009, reason) } catch (e: Exception) { logError("failSocket", e) }
            }

            override fun onOpen(ws: WebSocket, response: okhttp3.Response) {
                this.ws = ws
                open = true
                ws.send(hello)
            }

            override fun onMessage(ws: WebSocket, text: String) {
                handleText(text)
            }

            override fun onMessage(ws: WebSocket, bytes: okio.ByteString) {
                handleBinary(bytes.toByteArray())
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
                // #282-3: a single message over the read limit drops the socket
                // (Go SetReadLimit parity; control JSON is far below this).
                if (msg.length > MAX_WS_MSG) { failSocket("read limit exceeded"); return }
                try {
                    val m = Json.read(msg)
                    when (m["type"]) {
                        "welcome" -> {
                            // #338: PROTOCOL.md section 3 both-sides version
                            // gate - reject newer protocols loudly; missing
                            // proto = v1.
                            val p = (m["proto"] as? Number)?.toInt() ?: 1
                            if (p > 1) {
                                error = "proto_version: server speaks protocol v$p; update the SDK"
                                welcome.countDown()
                            } else {
                                info = m
                                welcome.countDown()
                            }
                        }
                        "error" -> { error = "${m["code"]}: ${m["message"] ?: ""}"; welcome.countDown() }
                        "req" -> {
                            val sid = (m["streamId"] as Number).toInt()
                            // #282-3: a replayed live streamId or a stream beyond
                            // the cap is refused with RST - never silently
                            // overwrite an in-flight stream.
                            if (streams.containsKey(sid) || streams.size >= maxStreams) {
                                ctl(0x03, sid); return
                            }
                            streams[sid] = m to ByteArrayOutputStream()
                        }
                        "ping" -> {
                            // literal-proof ts guard (#282-7): only numbers on the wire
                            val ts = (m["ts"] as? Number)?.toLong() ?: 0
                            ws.send("{\"type\":\"pong\",\"ts\":$ts}")
                        }
                        "bye" -> ws.close(1000, "server closed")
                    }
                } catch (e: Exception) {
                    logError("handleText", e) // #282-8: no silent swallow
                }
            }

            fun handleBinary(b: ByteArray) {
                if (b.size < ET_FRAME_HEADER) return
                if (b.size > MAX_WS_MSG) { failSocket("read limit exceeded"); return }
                try {
                    val kind = b[0].toInt() and 0xFF
                    val sid = ((b[1].toInt() and 0xFF) shl 24) or ((b[2].toInt() and 0xFF) shl 16) or
                        ((b[3].toInt() and 0xFF) shl 8) or (b[4].toInt() and 0xFF)
                    val len = ((b[5].toInt() and 0xFF) shl 24) or ((b[6].toInt() and 0xFF) shl 16) or
                        ((b[7].toInt() and 0xFF) shl 8) or (b[8].toInt() and 0xFF)
                    val st = streams[sid] ?: return
                    when (kind) {
                        0x01 -> {
                            val n = minOf(len, b.size - ET_FRAME_HEADER)
                            if (n > 0) st.second.write(b, ET_FRAME_HEADER, n)
                            // #282-3: buffered body over the cap -> RST, drop stream
                            if (st.second.size() > maxStreamBody) {
                                streams.remove(sid); ctl(0x03, sid)
                            }
                        }
                        0x02 -> {
                            streams.remove(sid)
                            val req = st.first
                            val body = st.second.toByteArray()
                            // #282-1: the replay runs on the dedicated executor,
                            // never on the WS reader thread - a slow local app
                            // must not starve pong processing (pingInterval=30s)
                            // or the server sees a dead tunnel and tears it down
                            // mid-request. Single thread keeps replay order.
                            replayPool.execute { serve(req, body, sid) }
                        }
                        0x03 -> streams.remove(sid)
                    }
                } catch (e: Exception) {
                    logError("handleBinary", e) // #282-8: no silent swallow
                }
            }

            @Suppress("UNCHECKED_CAST")
            fun serve(req: Map<String, Any?>, body: ByteArray, sid: Int) {
                try {
                    val method = req["method"] as String
                    var path = req["path"] as? String ?: "/"
                    var target = "http://${hostPort.first}:${hostPort.second}" // #282-5: host:port, not just 127.0.0.1
                    if (routes != null) {
                        val hit = routeFor(routes, path) ?: run { notFound(sid); return }
                        target = "http://${hit.first}"
                        path = hit.second
                    }
                    val rb = Request.Builder().url(target + path)
                    (req["headers"] as? Map<String, List<String>>)?.forEach { (k, vs) ->
                        val lk = k.lowercase()
                        if (lk != null && !HOP.contains(lk)) {
                            // #282-2: addHeader appends; header() would replace and
                            // drop all but the last value of multi-value headers
                            // (Cookie/X-Forwarded-For), which the TS baseline keeps.
                            vs.forEach { rb.addHeader(k, it) }
                        }
                    }
                    val noBody = method == "GET" || method == "HEAD"
                    rb.method(method,
                        if (noBody) null else body.toRequestBody(JSON))
                    val resp: okhttp3.Response
                    try {
                        resp = client.newCall(rb.build()).execute()
                    } catch (e: Exception) {
                        // #282-4: targetSdk>=28 hosts block cleartext to 127.0.0.1
                        // by default - surface a targeted hint through the log hook.
                        if ((e.message ?: "").contains("Cleartext")) {
                            logMsg("cleartext replay blocked by network security policy - allow it for 127.0.0.1 (see README 'Cleartext to localhost')")
                        }
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
                } catch (e: Exception) {
                    logError("replay sid=$sid", e) // #282-8
                    try { ws.send(ctlFrame(0x03, sid).toByteString()) } catch (e2: Exception) { logError("rst sid=$sid", e2) }
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
<footer>powered by <a href="https://etun.uk">etunnel</a></footer>
</main></body></html>"""
            }

            fun ctl(kind: Byte, sid: Int) { ws.send(ctlFrame(kind, sid).toByteString()) }
        }

        /** #282-5: "port" | "host:port" | "[v6]:port" | bare host (port 80). */
        internal fun pickHostPort(local: String): Pair<String, Int> {
            if (local.matches(Regex("\\d+"))) return "127.0.0.1" to local.toInt()
            val m = Regex("^(\\[[^]]+\\]|[^:]+):(\\d+)$").find(local)
            if (m != null) return m.groupValues[1] to m.groupValues[2].toInt()
            if (local.contains(':') && !local.startsWith("[")) return "[$local]" to 80
            return local to 80
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

        /** hello frame: client string derived from VERSION plus hello.version so
         *  the server can flag client_outdated. Extracted for unit testing
         *  (no device needed). */
        internal fun buildHello(subdomain: String?, token: String?, resume: String?): String = buildString {
            append("{\"type\":\"hello\",\"proto\":1")
            subdomain?.let { append(",\"subdomain\":\"${Json.esc(it)}\"") }
            token?.let { append(",\"token\":\"${Json.esc(it)}\"") }
            // resume: server-signed token from a previous welcome; the server then
            // hands back the same subdomain instead of allocating a new one.
            resume?.takeIf { it.isNotEmpty() }?.let { append(",\"resume\":\"${Json.esc(it)}\"") }
            append(",\"client\":\"etunnel-android/$VERSION\",\"version\":\"$VERSION\"}")
        }

        fun connect(server: String, localPort: Int, token: String? = null, subdomain: String? = null,
                    resume: String? = null, routes: List<Route>? = null): Etunnel {
            validateLocal(localPort.toString())
            return connect(server, localPort.toString(), token, subdomain, resume, routes)
        }

        /** `local` is a port number or "host:port" / "[v6]:port" (#282-5, the
         *  AGENTS.md contract allows host:port; bare host defaults to port 80). */
        fun connect(server: String, local: String, token: String? = null, subdomain: String? = null,
                    resume: String? = null, routes: List<Route>? = null): Etunnel {
            val norm = routes?.let { normalizeRoutes(it) }
            if (norm != null && norm.isEmpty()) throw IllegalArgumentException("etunnel: routes must not be empty")
            val hello = buildHello(subdomain, token, resume)
            val h = Handler(local, norm, hello)
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
            val t = Etunnel.connect(cfg.server, cfg.local, cfg.token, cfg.subdomain)
            println("TUNNEL_URL: ${t.url}")
        } catch (e: Exception) {
            System.err.println("etunnel: ${e.message}")
            kotlin.system.exitProcess(1)
        }
        // #282-9: self-join parked the main thread forever, even after the
        // tunnel dropped. Watch the live socket instead and exit when the
        // session ends (same liveness poll the reconnect loop uses).
        val h = Etunnel.lastHandler
        while (h != null && h.open) {
            try { Thread.sleep(200) } catch (ie: InterruptedException) { break }
        }
        return
    }

    val stopped = AtomicBoolean(false)
    Runtime.getRuntime().addShutdownHook(Thread({ stopped.set(true) }, "etunnel-shutdown"))

    var delay = 500L
    var resume = "" // server-signed token: reclaim the same subdomain on reconnect
    while (!stopped.get()) {
        var t: Etunnel? = null
        try {
            t = Etunnel.connect(cfg.server, cfg.local, cfg.token, cfg.subdomain, resume.ifEmpty { null })
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
