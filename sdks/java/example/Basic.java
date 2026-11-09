// Minimal etunnel example (Java 11+).
//   javac -d out src/main/java/dev/ggcode/Etunnel.java example/Basic.java
//   java -cp out dev.ggcode.Basic 3000
package dev.ggcode;

import com.sun.net.httpserver.HttpServer;
import java.net.InetSocketAddress;

public class Basic {
    public static void main(String[] args) throws Exception {
        int port = args.length > 0 ? Integer.parseInt(args[0]) : 3000;
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", port), 0);
        server.createContext("/", ex -> {
            byte[] body = "hello from etunnel-example\n".getBytes();
            ex.getResponseHeaders().add("content-type", "text/plain");
            ex.sendResponseHeaders(200, body.length);
            ex.getResponseBody().write(body);
            ex.close();
        });
        server.start();
        System.out.println("local http://127.0.0.1:" + port);
        Etunnel.Tunnel t = Etunnel.connect("wss://t.etun.uk/tunnel", port, null);
        System.out.println("public URL: " + t.url);
        Thread.sleep(Long.MAX_VALUE);
    }
}
