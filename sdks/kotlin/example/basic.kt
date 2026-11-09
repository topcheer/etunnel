// Minimal etunnel example (Kotlin, JDK 11+).
//   kotlinc Etunnel.kt example/basic.kt -include-runtime -d example.jar
//   java -jar example.jar 3000
import com.sun.net.httpserver.HttpServer
import java.net.InetSocketAddress

fun main(args: Array<String>) {
    val port = args.firstOrNull()?.toInt() ?: 3000
    val server = HttpServer.create(InetSocketAddress("127.0.0.1", port), 0)
    server.createContext("/") { ex ->
        val body = "hello from etunnel-example\n".toByteArray()
        ex.responseHeaders.add("content-type", "text/plain")
        ex.sendResponseHeaders(200, body.size.toLong())
        ex.responseBody.write(body)
        ex.close()
    }
    server.start()
    println("local http://127.0.0.1:$port")
    val t = Etunnel.connect("wss://t.etun.uk/tunnel", port)
    println("public URL: ${t.url}")
    t.await()
}
