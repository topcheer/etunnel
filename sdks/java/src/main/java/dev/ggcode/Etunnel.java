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
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;

// etunnel SDK for Java (11+, java.net.http only, zero dependencies).
// Buffer-based protocol client (bodies buffered per stream; no WS upgrades).
//
// Run:   java Etunnel.java 3000
// Embed: Etunnel.connect("wss://t.etun.uk/tunnel", 3000, null)
public class Etunnel {
    static final int MAXP = 32768;

    // Process-level shared clients (issue #51: connect() used to build two
    // fresh HttpClients per call and leaked them on error paths - on JDK < 21
    // their selector threads are non-daemon and pin the process). The local
    // client also bypasses any system proxy: 127.0.0.1 is always direct
    // (NO_PROXY; a global macOS proxy hijacking localhost is a real trap).
    static final HttpClient LOCAL_HTTP = HttpClient.newBuilder()
        .connectTimeout(Duration.ofSeconds(10))
        .proxy(java.net.ProxySelector.of(null))
        .build();
    static final HttpClient WS_HTTP = HttpClient.newHttpClient();

    public static class Options {
        public String token, subdomain, resume;
    }

    public static class Tunnel {
        public final String url, hostname, subdomain, plan, resumeToken;
        private final WebSocket ws;
        Tunnel(WebSocket ws, Map<String, Object> info) {
            this.ws = ws;
            this.url = str(info.get("url"));
            this.hostname = str(info.get("hostname"));
            this.subdomain = str(info.get("subdomain"));
            this.plan = str(info.get("plan"));
            this.resumeToken = str(info.get("resumeToken"));
        }
        public void close() {
            try { ws.sendText("{\"type\":\"bye\",\"reason\":\"client closed\"}", true); } catch (Exception ignored) {}
            ws.sendClose(WebSocket.NORMAL_CLOSURE, "bye");
        }
    }

    @SuppressWarnings("unchecked")
    public static Tunnel connect(String server, int localPort, Options opts) throws Exception {
        if (opts == null) opts = new Options();
        Handler h = new Handler(LOCAL_HTTP, localPort);

        WebSocket ws = WS_HTTP
            .newWebSocketBuilder()
            .subprotocols("etunnel-v1")
            .connectTimeout(Duration.ofSeconds(15))
            .buildAsync(URI.create(server), h)
            .join();
        h.ws = ws;

        StringBuilder hello = new StringBuilder("{\"type\":\"hello\",\"proto\":1");
        if (opts.subdomain != null) hello.append(",\"subdomain\":\"").append(jsonEsc(opts.subdomain)).append("\"");
        if (opts.token != null) hello.append(",\"token\":\"").append(jsonEsc(opts.token)).append("\"");
        // resume: server-signed token from a previous welcome; the server then
        // hands back the same subdomain instead of allocating a new one.
        if (opts.resume != null && !opts.resume.isEmpty()) {
            hello.append(",\"resume\":\"").append(jsonEsc(opts.resume)).append("\"");
        }
        hello.append(",\"client\":\"etunnel-java/1.4\"}");
        ws.sendText(hello.toString(), true).join();

        Map<String, Object> info = h.welcome.await(15, TimeUnit.SECONDS, ws);
        return new Tunnel(ws, info);
    }

    static String str(Object o) { return o == null ? "" : o.toString(); }

    /** Header-value JSON escaping. Order matters: backslashes first, then
     * quotes (issue #51: the old chained replace escaped quotes first, so a
     * value like a\"b became a\\\"b - invalid JSON). */
    static String hdrVal(String v) {
        return v.replace("\\", "\\\\").replace("\"", "\\\"");
    }

    /** Minimal JSON string escaping for control-message fields. */
    static String jsonEsc(String s) {
        StringBuilder b = new StringBuilder(s.length() + 8);
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            if (c == '"' || c == '\\') b.append('\\').append(c);
            else if (c == '\n') b.append("\\n");
            else if (c == '\r') b.append("\\r");
            else if (c == '\t') b.append("\\t");
            else b.append(c);
        }
        return b.toString();
    }

    static class WelcomeLatch {
        volatile Map<String, Object> value;
        volatile String error;
        final CountDownLatch latch = new CountDownLatch(1);
        Map<String, Object> await() throws Exception {
            latch.await();
            if (error != null) throw new RuntimeException("etunnel: " + error);
            return value;
        }
        /** Timed wait (issue #51: a bare await() could hang connect()
         * forever if the server never sends welcome). Closes ws on timeout. */
        Map<String, Object> await(long timeout, TimeUnit unit, WebSocket ws) throws Exception {
            if (!latch.await(timeout, unit)) {
                if (ws != null) {
                    try { ws.sendClose(WebSocket.NORMAL_CLOSURE, "welcome timeout"); } catch (Exception ignored) {}
                }
                throw new RuntimeException("etunnel: no welcome within " + timeout + " " + unit);
            }
            return await();
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
                    for (String v : vs) hb.append(",\"").append(k).append("\":[\"").append(hdrVal(v)).append("\"]");
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
            ws.sendText("{\"type\":\"res\",\"streamId\":" + sid
                + ",\"status\":502,\"headers\":{\"Content-Type\":[\"text/html; charset=utf-8\"]}}", true).join();
            byte[] body = localErrorPage(msg == null ? "error" : msg)
                .getBytes(java.nio.charset.StandardCharsets.UTF_8);
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

    /** Themed 502 page shown when the local app is down (same HTML as the other SDKs). */
    static final String ERR_PAGE_A =
        "<!doctype html><html><head><meta charset=\"utf-8\">\n"
        + "<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n"
        + "<title>502 - local app down</title>\n"
        + "<style>\n"
        + ":root{--bg:#0b0e14;--panel:#0d1117;--line:#21262d;--fg:#e6edf3;--dim:#8b949e;--green:#3fb950}\n"
        + "*{box-sizing:border-box;margin:0;padding:0}\n"
        + "body{font-family:ui-monospace,'SF Mono',Menlo,Consolas,monospace;background:var(--bg);color:var(--fg);\n"
        + "display:flex;min-height:100vh;align-items:center;justify-content:center;line-height:1.6}\n"
        + "main{max-width:600px;padding:2.5rem 1.5rem;width:100%}\n"
        + ".badge{display:inline-block;border:1px solid rgba(248,81,73,.4);color:#f85149;border-radius:99px;\n"
        + "padding:.2rem .8rem;font-size:.72rem;letter-spacing:.08em;text-transform:uppercase;margin-bottom:1.1rem}\n"
        + "h1{font-size:1.6rem;letter-spacing:-.01em;margin-bottom:.4rem}\n"
        + "h1 .g{color:var(--green)}\n"
        + "p{color:var(--dim);font-size:.9rem;margin-bottom:1.4rem}\n"
        + ".panel{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:1.1rem 1.3rem;margin-bottom:1.1rem}\n"
        + ".panel .k{color:var(--dim);font-size:.72rem;text-transform:uppercase;letter-spacing:.08em;margin-bottom:.3rem}\n"
        + ".panel .v{font-size:.9rem;word-break:break-all}\n"
        + ".err{color:#f85149}\n"
        + ".tip{border-left:3px solid var(--green);padding:.8rem 1rem;margin-top:1.2rem;font-size:.82rem;color:var(--dim)}\n"
        + ".tip b{color:var(--fg)}\n"
        + "footer{margin-top:2rem;font-size:.75rem;color:var(--dim)}\n"
        + "footer a{color:#58a6ff;text-decoration:none}\n"
        + ".pulse{display:inline-block;width:.55em;height:.55em;border-radius:50%;background:var(--green);\n"
        + "margin-right:.5em;animation:p 1.2s ease-in-out infinite}\n"
        + "@keyframes p{0%,100%{opacity:1}50%{opacity:.25}}\n"
        + "@media(prefers-reduced-motion:reduce){.pulse{animation:none}}\n"
        + "</style></head><body><main>\n"
        + "<span class=\"badge\">502 local app down</span>\n"
        + "<h1>tunnel up, <span class=\"g\">local app</span> is not</h1>\n"
        + "<p>The tunnel connection is alive, but nothing answered when we knocked.</p>\n"
        + "<div class=\"panel\">\n"
        + "<div class=\"k\">error</div><div class=\"v err\">";

    static final String ERR_PAGE_B =
        "</div>\n"
        + "</div>\n"
        + "<div class=\"tip\"><span class=\"pulse\"></span><b>Fix:</b> start your local service and reload -\n"
        + "the tunnel stays connected and keeps retrying for you.</div>\n"
        + "<footer>powered by <a href=\"https://github.com/topcheer/etunnel\">etunnel</a></footer>\n"
        + "</main></body></html>";

    static String localErrorPage(String msg) {
        String esc = msg.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;");
        return ERR_PAGE_A + esc + ERR_PAGE_B;
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

    static final AtomicBoolean STOPPED = new AtomicBoolean(false);

    /** Exponential backoff step with +/-20% jitter, capped at 30s. */
    static long nextDelay(long delay) {
        return Math.min(30000L, delay * 2);
    }

    static long jitter(long delay) {
        return (long) (delay * (0.8 + Math.random() * 0.4));
    }

    public static void main(String[] args) throws Exception {
        String server = System.getenv().getOrDefault("ETUNNEL_SERVER", "wss://t.etun.uk/tunnel");
        int port = Integer.parseInt(args.length > 0 ? args[0]
            : System.getenv().getOrDefault("ETUNNEL_LOCAL", "3000"));
        Options o = new Options();
        o.token = System.getenv("ETUNNEL_TOKEN");
        o.subdomain = System.getenv("ETUNNEL_SUBDOMAIN");

        if ("0".equals(System.getenv("ETUNNEL_RECONNECT"))) {
            // original single-shot behavior
            Tunnel t = connect(server, port, o);
            System.out.println("TUNNEL_URL: " + t.url);
            Thread.currentThread().join();
            return;
        }

        Runtime.getRuntime().addShutdownHook(new Thread(() -> STOPPED.set(true), "etunnel-shutdown"));

        long delay = 500;
        String resume = ""; // server-signed token: reclaim the same subdomain on reconnect
        while (!STOPPED.get()) {
            Tunnel t = null;
            o.resume = resume.isEmpty() ? null : resume;
            try {
                t = connect(server, port, o);
            } catch (Exception e) {
                System.err.println("etunnel: " + (e.getMessage() == null ? e.toString() : e.getMessage()));
            }
            if (t == null) {
                if (STOPPED.get()) break;
                try { Thread.sleep(jitter(delay)); } catch (InterruptedException ie) { break; }
                delay = nextDelay(delay);
                continue;
            }

            delay = 500; // reset after a successful connect
            if (!t.resumeToken.isEmpty()) resume = t.resumeToken;
            System.out.println("TUNNEL_URL: " + t.url);

            WebSocket ws = t.ws;
            while (!STOPPED.get() && !ws.isInputClosed() && !ws.isOutputClosed()) {
                try { Thread.sleep(200); } catch (InterruptedException ie) { break; }
            }
            if (STOPPED.get()) break;

            System.err.println("etunnel: disconnected, reconnecting");
            try { Thread.sleep(jitter(delay)); } catch (InterruptedException ie) { break; }
            delay = nextDelay(delay);
        }
    }
}
