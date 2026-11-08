// etunnel SDK for Scala (JDK 11+ java.net.http, zero dependencies).
// Buffer-based protocol client (bodies buffered per stream; no WS upgrades).
//
// Run:   scala Etunnel.scala 3000
// Embed: val t = Etunnel.connect("wss://t.ggcode.uk/tunnel", 3000)
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
  val MAXP = 32768
  private val HOP = Set("connection", "transfer-encoding", "keep-alive", "upgrade",
    "proxy-connection", "te", "trailer", "content-length", "host")

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
      ws.sendText(s"""{"type":"res","streamId":$sid,"status":502,"headers":{"Content-Type":["text/plain"]}}""", true).join()
      val body = s"etunnel: local service error: ${Option(msg).getOrElse("").replace("\"", "'")}\n".getBytes
      val f = new Array[Byte](9 + body.length)
      f(0) = 0x01
      putSid(f, sid); putLen(f, body.length)
      System.arraycopy(body, 0, f, 9, body.length)
      ws.sendBinary(ByteBuffer.wrap(f), true).join()
      ctl(0x02, sid)
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

  def connect(server: String, localPort: Int, token: String = null, subdomain: String = null): String = {
    val http = HttpClient.newBuilder().connectTimeout(Duration.ofSeconds(10)).build()
    val h = new Handler(http, localPort)
    val ws = HttpClient.newHttpClient()
      .newWebSocketBuilder()
      .subprotocols("etunnel-v1")
      .connectTimeout(Duration.ofSeconds(15))
      .buildAsync(URI.create(server), h)
      .join()
    h.ws = ws

    val hello = new StringBuilder("{\"type\":\"hello\",\"proto\":1")
    if (subdomain != null) hello.append(",\"subdomain\":\"").append(MiniJson.esc(subdomain)).append('"')
    if (token != null) hello.append(",\"token\":\"").append(MiniJson.esc(token)).append('"')
    hello.append(",\"client\":\"etunnel-scala/1.0\"}")
    ws.sendText(hello.toString, true).join()

    h.welcome.await()
    if (h.err != null) throw new RuntimeException(s"etunnel: ${h.err}")
    h.info("url").toString
  }
}

object Main {
  def main(args: Array[String]): Unit = {
    val server = sys.env.getOrElse("ETUNNEL_SERVER", "wss://t.ggcode.uk/tunnel")
    val port = (args.headOption orElse sys.env.get("ETUNNEL_LOCAL")).getOrElse("3000").toInt
    try {
      val url = Etunnel.connect(server, port, sys.env.get("ETUNNEL_TOKEN").orNull,
        sys.env.get("ETUNNEL_SUBDOMAIN").orNull)
      println(s"TUNNEL_URL: $url")
      Thread.currentThread().join()
    } catch {
      case e: Exception =>
        System.err.println(s"etunnel: ${e.getMessage}")
        sys.exit(1)
    }
  }
}
