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

class Etunnel private constructor(val url: String) {
    companion object {
        const val MAXP = 32768
        private val HOP = setOf("connection", "transfer-encoding", "keep-alive", "upgrade",
            "proxy-connection", "te", "trailer", "content-length", "host")

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
                ws.sendText("{\"type\":\"res\",\"streamId\":$sid,\"status\":502,\"headers\":{\"Content-Type\":[\"text/plain\"]}}", true).join()
                val body = "etunnel: local service error: ${msg?.replace("\"", "'")}\n".toByteArray()
                val f = ByteArray(9 + body.size)
                f[0] = 0x01
                putSid(f, sid); putLen(f, body.size)
                System.arraycopy(body, 0, f, 9, body.size)
                ws.sendBinary(ByteBuffer.wrap(f), true).join()
                ctl(0x02, sid)
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

        fun connect(server: String, localPort: Int, token: String? = null, subdomain: String? = null): Etunnel {
            val http = HttpClient.newBuilder().connectTimeout(Duration.ofSeconds(10)).build()
            val h = Handler(http, localPort)
            val ws = HttpClient.newHttpClient()
                .newWebSocketBuilder()
                .subprotocols("etunnel-v1")
                .connectTimeout(Duration.ofSeconds(15))
                .buildAsync(URI.create(server), h)
                .join()
            h.ws = ws

            val hello = buildString {
                append("{\"type\":\"hello\",\"proto\":1")
                subdomain?.let { append(",\"subdomain\":\"${Json.esc(it)}\"") }
                token?.let { append(",\"token\":\"${Json.esc(it)}\"") }
                append(",\"client\":\"etunnel-kotlin/1.0\"}")
            }
            ws.sendText(hello, true).join()

            h.welcome.await()
            h.error?.let { throw RuntimeException("etunnel: $it") }
            return Etunnel((h.info ?: error("no welcome"))["url"] as String)
        }
    }

    fun close() {
        // best effort; the tunnel also ends when the process exits
    }
}

fun main(args: Array<String>) {
    val server = System.getenv("ETUNNEL_SERVER") ?: "wss://t.etun.uk/tunnel"
    val port = (args.getOrNull(0) ?: System.getenv("ETUNNEL_LOCAL") ?: "3000").toInt()
    try {
        val t = Etunnel.connect(server, port,
            System.getenv("ETUNNEL_TOKEN"), System.getenv("ETUNNEL_SUBDOMAIN"))
        println("TUNNEL_URL: ${t.url}")
    } catch (e: Exception) {
        System.err.println("etunnel: ${e.message}")
        kotlin.system.exitProcess(1)
    }
    Thread.currentThread().join()
}
