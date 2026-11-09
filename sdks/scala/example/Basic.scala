// Minimal etunnel example (Scala 2.13/3, JDK 11+).
//   scalac -d out Etunnel.scala example/Basic.scala && scala -cp out Basic 3000
import com.sun.net.httpserver.HttpServer
import java.net.InetSocketAddress

object Basic {
  def main(args: Array[String]): Unit = {
    val port = args.headOption.map(_.toInt).getOrElse(3000)
    val server = HttpServer.create(new InetSocketAddress("127.0.0.1", port), 0)
    server.createContext("/", { ex =>
      val body = "hello from etunnel-example\n".getBytes
      ex.getResponseHeaders.add("content-type", "text/plain")
      ex.sendResponseHeaders(200, body.length.toLong)
      ex.getResponseBody.write(body)
      ex.close()
    })
    server.start()
    println(s"local http://127.0.0.1:$port")
    val t = Etunnel.connect("wss://t.etun.uk/tunnel", port)
    println(s"public URL: ${t.url}")
    t.await()
  }
}
