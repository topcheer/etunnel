package dev.ggcode;

import java.io.ByteArrayOutputStream;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.net.http.WebSocket;
import java.nio.ByteBuffer;
import java.time.Duration;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletionStage;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CountDownLatch;

// etunnel SDK for Java (11+, java.net.http only, zero dependencies).
// Buffer-based protocol client (bodies buffered per stream; no WS upgrades).
//
// Run:   java Etunnel.java 3000
// Embed: Etunnel.connect("wss://t.ggcode.uk/tunnel", 3000, null)
public class Etunnel {
    static final int MAXP = 32768;

    public static class Options {
        public String token, subdomain;
    }

    public static class Tunnel {
        public final String url, hostname, subdomain, plan;
        private final WebSocket ws;
        Tunnel(WebSocket ws, Map<String, Object> info) {
            this.ws = ws;
            this.url = str(info.get("url"));
            this.hostname = str(info.get("hostname"));
            this.subdomain = str(info.get("subdomain"));
            this.plan = str(info.get("plan"));
        }
        public void close() {
            try { ws.sendText("{\"type\":\"bye\",\"reason\":\"client closed\"}", true); } catch (Exception ignored) {}
            ws.sendClose(WebSocket.NORMAL_CLOSURE, "bye");
        }
    }

    @SuppressWarnings("unchecked")
    public static Tunnel connect(String server, int localPort, Options opts) throws Exception {
        if (opts == null) opts = new Options();
        HttpClient http = HttpClient.newBuilder().connectTimeout(Duration.ofSeconds(10)).build();
        Handler h = new Handler(http, localPort);

        WebSocket ws = HttpClient.newHttpClient()
            .newWebSocketBuilder()
            .subprotocols("etunnel-v1")
            .connectTimeout(Duration.ofSeconds(15))
            .buildAsync(URI.create(server), h)
            .join();
        h.ws = ws;

        StringBuilder hello = new StringBuilder("{\"type\":\"hello\",\"proto\":1");
        if (opts.subdomain != null) hello.append(",\"subdomain\":\"").append(opts.subdomain).append("\"");
        if (opts.token != null) hello.append(",\"token\":\"").append(opts.token).append("\"");
        hello.append(",\"client\":\"etunnel-java/1.0\"}");
        ws.sendText(hello.toString(), true).join();

        Map<String, Object> info = h.welcome.await();
        return new Tunnel(ws, info);
    }

    static String str(Object o) { return o == null ? "" : o.toString(); }

    static class WelcomeLatch {
        volatile Map<String, Object> value;
        volatile String error;
        final CountDownLatch latch = new CountDownLatch(1);
        Map<String, Object> await() throws Exception {
            latch.await();
            if (error != null) throw new RuntimeException("etunnel: " + error);
            return value;
        }
    }

    static class StreamBuf {
        Map<String, Object> req;
        final ByteArrayOutputStream body = new ByteArrayOutputStream();
    }

    static class Handler implements WebSocket.Listener {
        final HttpClient http;
        final int port;
        final WelcomeLatch welcome = new WelcomeLatch();
        final Map<Long, StreamBuf> streams = new ConcurrentHashMap<>();
        final StringBuilder textBuf = new StringBuilder();
        volatile WebSocket ws;

        Handler(HttpClient http, int port) { this.http = http; this.port = port; }

        @Override public void onOpen(WebSocket ws) { ws.request(1); }

        @Override
        public CompletionStage<?> onText(WebSocket ws, CharSequence data, boolean last) {
            textBuf.append(data);
            if (last) {
                String msg = textBuf.toString();
                textBuf.setLength(0);
                try { handleText(msg); } catch (Exception ignored) {}
            }
            ws.request(1);
            return null;
        }

        @Override
        public CompletionStage<?> onBinary(WebSocket ws, ByteBuffer data, boolean last) {
            byte[] b = new byte[data.remaining()];
            data.get(b);
            try { handleBinary(b); } catch (Exception ignored) {}
            ws.request(1);
            return null;
        }

        @Override
        public CompletionStage<?> onPing(WebSocket ws, ByteBuffer msg) {
            ws.sendPong(msg);
            ws.request(1);
            return null;
        }

        @SuppressWarnings("unchecked")
        void handleText(String msgRaw) {
            Map<String, Object> m = Json.read(msgRaw);
            String type = str(m.get("type"));
            if ("welcome".equals(type)) {
                welcome.value = m;
                welcome.latch.countDown();
            } else if ("error".equals(type)) {
                welcome.error = str(m.get("code")) + ": " + str(m.get("message"));
                welcome.latch.countDown();
            } else if ("req".equals(type)) {
                StreamBuf st = new StreamBuf();
                st.req = m;
                streams.put(((Number) m.get("streamId")).longValue(), st);
            } else if ("ping".equals(type)) {
                Object ts = m.get("ts");
                ws.sendText("{\"type\":\"pong\",\"ts\":" + (ts == null ? 0 : ts) + "}", true);
            } else if ("bye".equals(type)) {
                ws.sendClose(WebSocket.NORMAL_CLOSURE, "server closed");
            }
        }

        void handleBinary(byte[] b) {
            if (b.length < 9) return;
            int kind = b[0] & 0xFF;
            int sid = ((b[1] & 0xFF) << 24) | ((b[2] & 0xFF) << 16) | ((b[3] & 0xFF) << 8) | (b[4] & 0xFF);
            int len = ((b[5] & 0xFF) << 24) | ((b[6] & 0xFF) << 16) | ((b[7] & 0xFF) << 8) | (b[8] & 0xFF);
            int avail = Math.min(len, Math.max(0, b.length - 9));
            StreamBuf st = streams.get((long) sid);
            if (st == null) return;
            if (kind == 0x01) st.body.write(b, 9, avail);
            else if (kind == 0x02) { streams.remove((long) sid); serve(st, sid); }
            else if (kind == 0x03) streams.remove((long) sid);
        }

        @SuppressWarnings("unchecked")
        void serve(StreamBuf st, int sid) {
            try {
                Map<String, Object> req = st.req;
                String method = str(req.get("method"));
                String path = str(req.get("path"));
                byte[] body = st.body.toByteArray();

                HttpRequest.Builder rb = HttpRequest.newBuilder()
                    .uri(URI.create("http://127.0.0.1:" + port + path))
                    .timeout(Duration.ofSeconds(120));
                Map<String, List<String>> headers = (Map<String, List<String>>) req.get("headers");
                if (headers != null) {
                    for (Map.Entry<String, List<String>> e : headers.entrySet()) {
                        String k = e.getKey();
                        if (k == null) continue;
                        String lk = k.toLowerCase();
                        if (lk.equals("connection") || lk.equals("transfer-encoding") || lk.equals("keep-alive")
                            || lk.equals("upgrade") || lk.equals("proxy-connection") || lk.equals("te")
                            || lk.equals("trailer") || lk.equals("content-length") || lk.equals("host")) continue;
                        for (String v : e.getValue()) rb.header(k, v);
                    }
                }
                boolean noBody = method.equals("GET") || method.equals("HEAD");
                rb.method(method, noBody ? HttpRequest.BodyPublishers.noBody()
                                         : HttpRequest.BodyPublishers.ofByteArray(body));
                HttpResponse<byte[]> resp;
                try {
                    resp = http.send(rb.build(), HttpResponse.BodyHandlers.ofByteArray());
                } catch (Exception e) {
                    localError(sid, e.getMessage());
                    return;
                }
                StringBuilder hb = new StringBuilder();
                resp.headers().map().forEach((k, vs) -> {
                    String lk = k.toLowerCase();
                    if (lk.equals("connection") || lk.equals("transfer-encoding") || lk.equals("content-length")
                        || lk.equals("keep-alive") || lk.equals("upgrade")) return;
                    for (String v : vs) hb.append(",\"").append(k).append("\":[\"")
                        .append(v.replace("\"", "\\\"").replace("\\", "\\\\")).append("\"]");
                });
                ws.sendText("{\"type\":\"res\",\"streamId\":" + sid + ",\"status\":" + resp.statusCode()
                    + ",\"headers\":{" + hb.substring(Math.min(1, hb.length())) + "}}", true).join();
                byte[] payload = resp.body();
                for (int i = 0; i < payload.length; i += MAXP) {
                    int n = Math.min(MAXP, payload.length - i);
                    byte[] f = new byte[9 + n];
                    f[0] = 0x01;
                    f[1] = (byte) (sid >>> 24); f[2] = (byte) (sid >>> 16); f[3] = (byte) (sid >>> 8); f[4] = (byte) sid;
                    f[5] = (byte) (n >>> 24); f[6] = (byte) (n >>> 16); f[7] = (byte) (n >>> 8); f[8] = (byte) n;
                    System.arraycopy(payload, i, f, 9, n);
                    ws.sendBinary(ByteBuffer.wrap(f), true).join();
                }
                sendCtlFrame((byte) 0x02, sid);
            } catch (Exception e) {
                try { sendCtlFrame((byte) 0x03, sid); } catch (Exception ignored) {}
            }
        }

        void localError(int sid, String msg) {
            String safe = msg == null ? "" : msg.replace("\"", "'");
            ws.sendText("{\"type\":\"res\",\"streamId\":" + sid
                + ",\"status\":502,\"headers\":{\"Content-Type\":[\"text/plain\"]}}", true).join();
            byte[] body = ("etunnel: local service error: " + safe + "\n").getBytes(java.nio.charset.StandardCharsets.UTF_8);
            byte[] f = new byte[9 + body.length];
            f[0] = 0x01;
            f[1] = (byte) (sid >>> 24); f[2] = (byte) (sid >>> 16); f[3] = (byte) (sid >>> 8); f[4] = (byte) sid;
            f[5] = (byte) (body.length >>> 24); f[6] = (byte) (body.length >>> 16); f[7] = (byte) (body.length >>> 8); f[8] = (byte) body.length;
            System.arraycopy(body, 0, f, 9, body.length);
            ws.sendBinary(ByteBuffer.wrap(f), true).join();
            sendCtlFrame((byte) 0x02, sid);
        }

        void sendCtlFrame(byte kind, int sid) {
            byte[] f = new byte[9];
            f[0] = kind;
            f[1] = (byte) (sid >>> 24); f[2] = (byte) (sid >>> 16); f[3] = (byte) (sid >>> 8); f[4] = (byte) sid;
            ws.sendBinary(ByteBuffer.wrap(f), true).join();
        }
    }

    /** Tiny JSON reader (objects/arrays/strings/numbers only). */
    static class Json {
        @SuppressWarnings("unchecked")
        static Map<String, Object> read(String s) {
            P p = new P(s);
            p.skip();
            return (Map<String, Object>) p.parse();
        }
        static class P {
            final String s; int i;
            P(String s) { this.s = s; }
            Object parse() {
                skip();
                char c = s.charAt(i);
                if (c == '{') {
                    Map<String, Object> m = new HashMap<>();
                    i++; skip();
                    if (s.charAt(i) == '}') { i++; return m; }
                    while (true) {
                        skip();
                        String k = (String) parse(); skip(); i++;
                        m.put(k, parse()); skip();
                        if (s.charAt(i) == ',') { i++; continue; }
                        i++; break;
                    }
                    return m;
                }
                if (c == '[') {
                    List<Object> l = new ArrayList<>();
                    i++; skip();
                    if (s.charAt(i) == ']') { i++; return l; }
                    while (true) {
                        l.add(parse()); skip();
                        if (s.charAt(i) == ',') { i++; continue; }
                        i++; break;
                    }
                    return l;
                }
                if (c == '"') {
                    StringBuilder b = new StringBuilder(); i++;
                    while (s.charAt(i) != '"') {
                        if (s.charAt(i) == '\\') {
                            char n = s.charAt(i + 1);
                            if (n == 'n') b.append('\n');
                            else if (n == 't') b.append('\t');
                            else if (n == 'u') { b.append((char) Integer.parseInt(s.substring(i + 2, i + 6), 16)); i += 4; }
                            else b.append(n);
                            i += 2;
                        } else { b.append(s.charAt(i)); i++; }
                    }
                    i++; return b.toString();
                }
                int j = i;
                while (j < s.length() && "+-.0123456789eE".indexOf(s.charAt(j)) >= 0) j++;
                String num = s.substring(i, j);
                i = j;
                if (num.contains(".") || num.contains("e") || num.contains("E")) return Double.parseDouble(num);
                try { return Long.parseLong(num); } catch (NumberFormatException e) { return num; }
            }
            void skip() { while (i < s.length() && Character.isWhitespace(s.charAt(i))) i++; }
        }
    }

    public static void main(String[] args) throws Exception {
        String server = System.getenv().getOrDefault("ETUNNEL_SERVER", "wss://t.ggcode.uk/tunnel");
        int port = Integer.parseInt(args.length > 0 ? args[0]
            : System.getenv().getOrDefault("ETUNNEL_LOCAL", "3000"));
        Options o = new Options();
        o.token = System.getenv("ETUNNEL_TOKEN");
        o.subdomain = System.getenv("ETUNNEL_SUBDOMAIN");
        Tunnel t = connect(server, port, o);
        System.out.println("TUNNEL_URL: " + t.url);
        Thread.currentThread().join();
    }
}
