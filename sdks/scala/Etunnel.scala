// etunnel SDK for Scala (JDK 11+ java.net.http, zero dependencies).
// Buffer-based protocol client (bodies buffered per stream; no WS upgrades).
//
// Run:   scala Etunnel.scala 3000
// Embed: val t = Etunnel.connect("wss://t.etun.uk/tunnel", 3000)
//        println(t.url)
import java.io.ByteArrayOutputStream
import java.net.URI
import java.net.http.{HttpClient, HttpRequest, HttpResponse, WebSocket}
import java.nio.ByteBuffer
import java.time.Duration
import java.util.concurrent.{CompletionStage, ConcurrentHashMap, CountDownLatch}
import scala.collection.mutable

object MiniJson {
  // Minimal JSON parser for flat control messages (objects/arrays/strings/numbers).
  def read(s: String): Map[String, Any] = {
    val p = new P(s)
    p.parse().asInstanceOf[Map[String, Any]]
  }

  private class P(val s: String) {
    var i = 0
    def parse(): Any = {
      skip()
      s(i) match {
        case '{' =>
          i += 1; skip()
          val m = mutable.Map[String, Any]()
          if (s(i) == '}') { i += 1; return m.toMap }
          while (true) {
            skip()
            val k = parse().asInstanceOf[String]
            skip(); i += 1 // ':'
            m(k) = parse()
            skip()
            if (s(i) == ',') { i += 1 } else { i += 1; return m.toMap }
          }
          m.toMap
        case '[' =>
          i += 1; skip()
          val l = mutable.ListBuffer[Any]()
          if (s(i) == ']') { i += 1; return l.toList }
          while (true) {
            l += parse()
            skip()
            if (s(i) == ',') { i += 1 } else { i += 1; return l.toList }
          }
          l.toList
        case '"' =>
          val b = new StringBuilder
          i += 1
          while (s(i) != '"') {
            if (s(i) == '\\') {
              s(i + 1) match {
                case 'n' => b += '\n'
                case 't' => b += '\t'
                case 'u' => b += Integer.parseInt(s.substring(i + 2, i + 6), 16).toChar; i += 4
                case c => b += c
              }
              i += 2
            } else { b += s(i); i += 1 }
          }
          i += 1
          b.toString
        case _ =>
          val j = i
          while (i < s.length && "+-.0123456789eE".indexOf(s(i)) >= 0) i += 1
          val num = s.substring(j, i)
          try num.toLong catch { case _: NumberFormatException => num.toDouble }
      }
    }
    def skip(): Unit = while (i < s.length && s(i).isWhitespace) i += 1
  }

  def esc(x: String): String =
    x.replace("\\", "\\\\").replace("\"", "\\\"").replace("\n", "\\n")
}

object Etunnel {
  /** A live tunnel: the assigned URL plus the token that reclaims it on reconnect. */
  case class Tunnel(url: String, resumeToken: String)

  val MAXP = 32768
  private val HOP = Set("connection", "transfer-encoding", "keep-alive", "upgrade",
    "proxy-connection", "te", "trailer", "content-length", "host")

  // Handle of the last established socket, so the CLI can watch for a disconnect.
  @volatile var lastWs: WebSocket = _

  class Handler(http: HttpClient, port: Int) extends WebSocket.Listener {
    val welcome = new CountDownLatch(1)
    @volatile var info: Map[String, Any] = _
    @volatile var err: String = _
    val streams = new ConcurrentHashMap[Int, (Map[String, Any], ByteArrayOutputStream)]()
    val textBuf = new StringBuilder
    @volatile var ws: WebSocket = _

    override def onOpen(ws: WebSocket): Unit = ws.request(1)

    override def onText(ws: WebSocket, data: CharSequence, last: Boolean): CompletionStage[_] = {
      textBuf.append(data)
      if (last) {
        val msg = textBuf.toString
        textBuf.setLength(0)
        try handleText(msg) catch { case _: Exception => () }
      }
      ws.request(1)
      null
    }

    override def onBinary(ws: WebSocket, data: ByteBuffer, last: Boolean): CompletionStage[_] = {
      val b = new Array[Byte](data.remaining())
      data.get(b)
      try handleBinary(b) catch { case _: Exception => () }
      ws.request(1)
      null
    }

    override def onPing(ws: WebSocket, message: ByteBuffer): CompletionStage[_] = {
      ws.sendPong(message)
      ws.request(1)
      null
    }

    def handleText(msg: String): Unit = {
      val m = MiniJson.read(msg)
      m.get("type").foreach {
        case "welcome" => info = m; welcome.countDown()
        case "error"   => err = s"${m("code")}: ${m.getOrElse("message", "")}"; welcome.countDown()
        case "req" =>
          val sid = m("streamId").toString.toLong.toInt
          streams.put(sid, (m, new ByteArrayOutputStream()))
        case "ping" => ws.sendText(s"""{"type":"pong","ts":${m.getOrElse("ts", 0L)}}""", true)
        case "bye"  => ws.sendClose(WebSocket.NORMAL_CLOSURE, "server closed")
        case _ =>
      }
    }

    def handleBinary(b: Array[Byte]): Unit = {
      if (b.length < 9) return
      val kind = b(0) & 0xFF
      val sid = ((b(1) & 0xFF) << 24) | ((b(2) & 0xFF) << 16) | ((b(3) & 0xFF) << 8) | (b(4) & 0xFF)
      val st = streams.get(sid)
      if (st == null) return
      kind match {
        case 0x01 => st._2.write(b, 9, b.length - 9)
        case 0x02 =>
          streams.remove(sid)
          serve(st._1, st._2.toByteArray, sid)
        case 0x03 => streams.remove(sid)
        case _ =>
      }
    }

    def serve(req: Map[String, Any], body: Array[Byte], sid: Int): Unit = {
      try {
        val method = req("method").toString
        val rb = HttpRequest.newBuilder()
          .uri(URI.create(s"http://127.0.0.1:$port${req("path")}"))
          .timeout(Duration.ofSeconds(120))
        req.get("headers").foreach { h =>
          h.asInstanceOf[Map[String, List[String]]].foreach { case (k, vs) =>
            if (k != null && !HOP.contains(k.toLowerCase)) vs.foreach(v => rb.header(k, v))
          }
        }
        val noBody = method == "GET" || method == "HEAD"
        rb.method(method,
          if (noBody) HttpRequest.BodyPublishers.noBody() else HttpRequest.BodyPublishers.ofByteArray(body))
        val resp =
          try http.send(rb.build(), HttpResponse.BodyHandlers.ofByteArray())
          catch { case e: Exception =>
            localError(sid, e.getMessage)
            return
          }
        val sb = new StringBuilder
        var first = true
        resp.headers().map.forEach { (k: String, vs: java.util.List[String]) =>
          val lk = k.toLowerCase
          if (!HOP(lk) && lk != "content-length") {
            vs.forEach { v: String =>
              if (!first) sb.append(',')
              first = false
              sb.append('"').append(MiniJson.esc(k)).append("\":[\"")
                .append(MiniJson.esc(v)).append("\"]")
            }
          }
        }
        val hdrJson = sb.toString
        ws.sendText("{\"type\":\"res\",\"streamId\":" + sid + ",\"status\":" + resp.statusCode()
          + ",\"headers\":{" + hdrJson + "}}", true).join()
        val payload = resp.body()
        var i = 0
        while (i < payload.length) {
          val n = math.min(MAXP, payload.length - i)
          val f = new Array[Byte](9 + n)
          f(0) = 0x01
          putSid(f, sid); putLen(f, n)
          System.arraycopy(payload, i, f, 9, n)
          ws.sendBinary(ByteBuffer.wrap(f), true).join()
          i += n
        }
        ctl(0x02, sid)
      } catch {
        case _: Exception => try ctl(0x03, sid) catch { case _: Exception => () }
      }
    }

    def localError(sid: Int, msg: String): Unit = {
      ws.sendText(s"""{"type":"res","streamId":$sid,"status":502,"headers":{"Content-Type":["text/html; charset=utf-8"]}}""", true).join()
      val body = localErrorPage(Option(msg).getOrElse("error")).getBytes
      val f = new Array[Byte](9 + body.length)
      f(0) = 0x01
      putSid(f, sid); putLen(f, body.length)
      System.arraycopy(body, 0, f, 9, body.length)
      ws.sendBinary(ByteBuffer.wrap(f), true).join()
      ctl(0x02, sid)
    }

    // Themed 502 page shown when the local app is down (same HTML as the other SDKs).
    def localErrorPage(msg: String): String = {
      val esc = msg.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
      s"""<!doctype html><html><head><meta charset="utf-8">
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

    def ctl(kind: Byte, sid: Int): Unit = {
      val f = new Array[Byte](9)
      f(0) = kind
      putSid(f, sid)
      ws.sendBinary(ByteBuffer.wrap(f), true).join()
    }

    def putSid(f: Array[Byte], sid: Int): Unit = {
      f(1) = (sid >>> 24).toByte; f(2) = (sid >>> 16).toByte
      f(3) = (sid >>> 8).toByte; f(4) = sid.toByte
    }

    def putLen(f: Array[Byte], n: Int): Unit = {
      f(5) = (n >>> 24).toByte; f(6) = (n >>> 16).toByte
      f(7) = (n >>> 8).toByte; f(8) = n.toByte
    }
  }

  private val localHttp = HttpClient.newBuilder()
    .connectTimeout(Duration.ofSeconds(10))
    .proxy(java.net.ProxySelector.of(null))
    .build()
  private val wsHttp = HttpClient.newBuilder()
    .connectTimeout(Duration.ofSeconds(15))
    .build()

  def connect(server: String, localPort: Int, token: String = null, subdomain: String = null,
              resume: String = null): Tunnel = {
    val http = localHttp
    val h = new Handler(http, localPort)
    val ws = wsHttp
      .newWebSocketBuilder()
      .subprotocols("etunnel-v1")
      .connectTimeout(Duration.ofSeconds(15))
      .buildAsync(URI.create(server), h)
      .join()
    h.ws = ws
    lastWs = ws

    val hello = new StringBuilder("{\"type\":\"hello\",\"proto\":1")
    if (subdomain != null) hello.append(",\"subdomain\":\"").append(MiniJson.esc(subdomain)).append('"')
    if (token != null) hello.append(",\"token\":\"").append(MiniJson.esc(token)).append('"')
    // resume: server-signed token from a previous welcome; the server then
    // hands back the same subdomain instead of allocating a new one.
    if (resume != null && resume.nonEmpty)
      hello.append(",\"resume\":\"").append(MiniJson.esc(resume)).append('"')
    hello.append(",\"client\":\"etunnel-scala/1.4\"}")
    ws.sendText(hello.toString, true).join()

    // Timed wait (issue #51): a bare await() hung forever if the server never
    // sent welcome after completing the handshake.
    if (!h.welcome.await(15, java.util.concurrent.TimeUnit.SECONDS)) {
      try ws.sendClose(WebSocket.NORMAL_CLOSURE, "welcome timeout") catch { case _: Exception => }
      throw new RuntimeException("etunnel: no welcome within 15 SECONDS")
    }
    if (h.err != null) throw new RuntimeException(s"etunnel: ${h.err}")
    Tunnel(h.info("url").toString, h.info.get("resumeToken").map(_.toString).getOrElse(""))
  }
}

object Main {
  /** Exponential backoff step, capped at 30s. */
  private def nextDelay(delay: Long): Long = math.min(30000L, delay * 2)

  /** +/-20% jitter so many clients don't retry in lockstep. */
  private def jitter(delay: Long): Long = (delay * (0.8 + scala.util.Random.nextDouble() * 0.4)).toLong

  private def closed(ws: WebSocket): Boolean = ws == null || ws.isInputClosed || ws.isOutputClosed

  def main(args: Array[String]): Unit = {
    val server = sys.env.getOrElse("ETUNNEL_SERVER", "wss://t.etun.uk/tunnel")
    val port = (args.headOption orElse sys.env.get("ETUNNEL_LOCAL")).getOrElse("3000").toInt
    val token = sys.env.get("ETUNNEL_TOKEN").orNull
    val subdomain = sys.env.get("ETUNNEL_SUBDOMAIN").orNull

    if (sys.env.get("ETUNNEL_RECONNECT").contains("0")) {
      // original single-shot behavior
      try {
        val url = Etunnel.connect(server, port, token, subdomain)
        println(s"TUNNEL_URL: $url")
      } catch {
        case e: Exception =>
          System.err.println(s"etunnel: ${e.getMessage}")
          sys.exit(1)
      }
      Thread.currentThread().join()
      return
    }

    val stopped = new java.util.concurrent.atomic.AtomicBoolean(false)
    Runtime.getRuntime.addShutdownHook(new Thread(() => stopped.set(true), "etunnel-shutdown"))

    var delay = 500L
    var resume = "" // server-signed token: reclaim the same subdomain on reconnect
    while (!stopped.get()) {
      var tunnel: Etunnel.Tunnel = null
      try tunnel = Etunnel.connect(server, port, token, subdomain, if (resume.isEmpty) null else resume)
      catch { case e: Exception => System.err.println(s"etunnel: ${e.getMessage}") }

      if (tunnel == null) {
        if (stopped.get()) return
        try Thread.sleep(jitter(delay)) catch { case _: InterruptedException => return }
        delay = nextDelay(delay)
      } else {
        delay = 500L // reset after a successful connect
        if (tunnel.resumeToken.nonEmpty) resume = tunnel.resumeToken
        println(s"TUNNEL_URL: ${tunnel.url}")

        val ws = Etunnel.lastWs
        while (!stopped.get() && !closed(ws)) {
          try Thread.sleep(200) catch { case _: InterruptedException => return }
        }
        if (stopped.get()) return

        System.err.println("etunnel: disconnected, reconnecting")
        try Thread.sleep(jitter(delay)) catch { case _: InterruptedException => return }
        delay = nextDelay(delay)
      }
    }
  }
}
