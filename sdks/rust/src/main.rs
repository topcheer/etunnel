// etunnel SDK for Rust (tungstenite + ureq).
// Buffer-based protocol client (bodies buffered per stream; no WS upgrades).
use std::collections::HashMap;
use std::collections::hash_map::RandomState;
use std::hash::{BuildHasher, Hasher};
use std::io::Read;
use std::net::TcpStream;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc;
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::Duration;

use tungstenite::client::IntoClientRequest;
use tungstenite::handshake::client::Response;
use tungstenite::protocol::{Message, WebSocketConfig};
use tungstenite::stream::MaybeTlsStream;

const MAXP: usize = 32768;
/// Read tick for the socket: how long a blocking read may hold the lock
/// before the loop drains outbound messages (#53).
const READ_TICK_MS: u64 = 200;

// #243 M2: Go-parity resource limits (sdk/stream.go: maxClientStreams=256,
// SetReadLimit(128KiB)). A hostile or buggy server must not be able to OOM
// the client: refuse streams over the cap with an RST, RST a stream whose
// buffered request body passes MAX_STREAM_BODY, drop the socket on a single
// ws message above the read limit.
const MAX_STREAMS: usize = 256;
const MAX_STREAM_BODY: usize = 16 * 1024 * 1024;
const MAX_WS_MSG: usize = 128 * 1024;
// #243 M3: client-side keepalive (Go pingLoop parity) - ping every 30s and
// drop the tunnel when neither pongs nor traffic arrive for 150s; without it
// a NAT blackhole keeps the client believing the tunnel is alive.
const PING_INTERVAL_SECS: u64 = 30;
const PING_DEADLINE_SECS: u64 = 150;

pub struct Options {
    pub token: Option<String>,
    pub subdomain: Option<String>,
}

pub struct Tunnel {
    pub url: String,
    /// Server-signed resume token: pass it back on reconnect to reclaim the
    /// same subdomain instead of getting a fresh random one.
    pub resume_token: String,
    /// Set once the reader loop sees the connection end (error, close or bye).
    /// The CLI supervisor polls it to decide when to reconnect.
    pub closed: Arc<AtomicBool>,
    /// Outbound relay handle for close() (#243 M8): sending a bye here is
    /// flushed by the reader loop, which owns the socket lock.
    out: Option<mpsc::Sender<Message>>,
}

impl Tunnel {
    /// Close tells the server we are going away (bye) so the subdomain is
    /// freed promptly, then marks the tunnel closed; the reader flushes the
    /// queue and exits when the server hangs up. Previously a no-op that
    /// leaked the connection until process exit (#243 M8).
    pub fn close(&self) {
        if let Some(tx) = &self.out {
            tx.send(Message::Text(
                "{\"type\":\"bye\",\"reason\":\"client close\"}".into(),
            ))
            .ok();
        }
        self.closed.store(true, Ordering::SeqCst);
    }
}

fn frame(kind: u8, sid: u32, payload: &[u8]) -> Message {
    let mut b = Vec::with_capacity(9 + payload.len());
    b.push(kind);
    b.extend_from_slice(&sid.to_be_bytes());
    b.extend_from_slice(&(payload.len() as u32).to_be_bytes());
    b.extend_from_slice(payload);
    Message::Binary(b.into())
}

// Minimal JSON helpers (control messages are simple, hand-rolled is enough).
fn json_escape(s: &str) -> String {
    s.replace('\\', "\\\\").replace('"', "\\\"").replace('\n', "\\n")
}

fn extract_str(json: &str, key: &str) -> Option<String> {
    let pat = format!("\"{}\":", key);
    let i = json.find(&pat)? + pat.len();
    let rest = &json[i..];
    let rest = rest.trim_start();
    if !rest.starts_with('"') {
        return None;
    }
    let mut out = String::new();
    let mut chars = rest[1..].chars();
    while let Some(c) = chars.next() {
        match c {
            '"' => break,
            '\\' => match chars.next() {
                Some('n') => out.push('\n'),
                Some('t') => out.push('\t'),
                Some(o) => out.push(o),
                None => break,
            },
            o => out.push(o),
        }
    }
    Some(out)
}

fn extract_num(json: &str, key: &str) -> Option<f64> {
    let pat = format!("\"{}\":", key);
    let i = json.find(&pat)? + pat.len();
    let rest = json[i..].trim_start();
    let end = rest
        .find(|c: char| !(c.is_ascii_digit() || c == '.' || c == '-'))
        .unwrap_or(rest.len());
    rest[..end].parse().ok()
}

// Very small header-map extractor for req messages: "headers":{"K":["v"],...}
fn extract_headers(json: &str) -> HashMap<String, Vec<String>> {
    let mut out = HashMap::new();
    let Some(hstart) = json.find("\"headers\":{") else { return out };
    let body = &json[hstart + 11..];
    // walk pairs until the matching close brace (naive but messages are flat).
    // Iterate chars, not bytes: `byte as char` re-encoded multi-byte UTF-8
    // header values as Latin-1 mojibake (#53 LOW).
    let chars: Vec<char> = body.chars().collect();
    let mut i = 0usize;
    let mut depth = 1usize;
    let mut cur_key = String::new();
    let mut cur_vals: Vec<String> = Vec::new();
    let mut in_key = false;
    let mut in_val = false;
    let mut buf = String::new();
    while i < chars.len() {
        let c = chars[i];
        match c {
            '"' => {
                if in_key || in_val {
                    if in_key { cur_key = buf.clone(); } else { cur_vals.push(buf.clone()); }
                    buf.clear();
                    in_key = false;
                    in_val = false;
                } else if cur_key.is_empty() {
                    in_key = true;
                } else {
                    in_val = true;
                }
            }
            '{' | '[' => depth += 1,
            '}' | ']' => {
                depth -= 1;
                if depth == 0 {
                    if !cur_key.is_empty() {
                        out.insert(cur_key.clone(), cur_vals.clone());
                    }
                    break;
                }
            }
            ',' if depth == 1 && !cur_key.is_empty() && cur_vals.is_empty() => {
                // next key after value list ends is handled by ':' ... fallthrough
            }
            ':' if depth == 1 && !cur_key.is_empty() && cur_vals.is_empty() => {}
            _ => {
                if in_key || in_val {
                    buf.push(c);
                }
            }
        }
        i += 1;
    }
    out
}

struct StreamBuf {
    method: String,
    path: String,
    headers: HashMap<String, Vec<String>>,
    body: Vec<u8>,
    size: usize, // #243 M2: buffered request-body byte count
}

pub fn connect(server: &str, local_port: u16, token: Option<&str>, subdomain: Option<&str>) -> Result<Tunnel, String> {
    connect_full(server, &format!("127.0.0.1:{}", local_port), token, subdomain)
}

pub fn connect_full(server: &str, local_addr: &str, token: Option<&str>, subdomain: Option<&str>) -> Result<Tunnel, String> {
    connect_local(server, Local::One(local_addr.to_string()), token, subdomain, None)
}

/// Route-aware connect: several local services under one public hostname
/// dispatched by URL path prefix (see route.rs).
pub fn connect_routes(server: &str, routes: Vec<Route>, token: Option<&str>, subdomain: Option<&str>, resume: Option<&str>) -> Result<Tunnel, String> {
    let mut routes = routes;
    route::normalize(&mut routes)?;
    connect_local(server, Local::Routes(routes), token, subdomain, resume)
}

/// Like [`connect_full`], plus an optional resume token from a previous
/// session so the server hands back the same subdomain after a drop.
pub fn connect_full_resume(server: &str, local_addr: &str, token: Option<&str>, subdomain: Option<&str>, resume: Option<&str>) -> Result<Tunnel, String> {
    connect_local(server, Local::One(local_addr.to_string()), token, subdomain, resume)
}

fn connect_local(server: &str, local: Local, token: Option<&str>, subdomain: Option<&str>, resume: Option<&str>) -> Result<Tunnel, String> {
    let mut request = server
        .into_client_request()
        .map_err(|e| format!("etunnel: bad url: {}", e))?;
    request.headers_mut().insert(
        "Sec-WebSocket-Protocol",
        "etunnel-v1".parse().unwrap(),
    );
    // #243 M2: enforce the 128KiB read limit at the transport - a single ws
    // message above it is a protocol violation and the read errors out,
    // tearing the tunnel down (Go SetReadLimit parity).
    let cfg = WebSocketConfig::default()
        .max_message_size(Some(MAX_WS_MSG))
        .max_frame_size(Some(MAX_WS_MSG));
    let (ws, _resp) = tungstenite::client::connect_with_config(request, Some(cfg), 3)
        .map_err(|e| format!("etunnel: connect: {}", e))?;

    // Bound every blocking read so the reader periodically releases the
    // socket lock and drains the outbound queue: holding the lock across an
    // unbounded read() blocked every sender (serve threads, pong) until the
    // next inbound frame arrived (#53 MED). MaybeTlsStream has no blanket
    // timeout setter, so drill into the concrete stream it wraps.
    //
    // #242: the tick is applied AFTER the welcome exchange. The handshake
    // loop below does not treat WouldBlock/TimedOut as retryable, so a 200ms
    // tick during the handshake turned any welcome that arrived later than
    // 200ms (cross-geo latency, busy server) into "read welcome: TimedOut" -
    // the Rust CLI was effectively unusable off localhost. During the
    // handshake we want no tick at all; the steady-state reader spawned
    // further down DOES handle the tick (it drops the lock and flushes), so
    // it keeps the #53 guarantee.
    fn apply_tick(ws: &tungstenite::WebSocket<MaybeTlsStream<TcpStream>>, tick: Option<Duration>) {
        let res = match ws.get_ref() {
            MaybeTlsStream::Plain(s) => s.set_read_timeout(tick),
            MaybeTlsStream::Rustls(s) => s.get_ref().set_read_timeout(tick),
            // other TLS backends: best-effort, the queue still drains between
            // frames whenever the peer is active
            #[allow(unreachable_patterns)]
            _ => Ok(()),
        };
        if let Err(e) = res {
            eprintln!("etunnel: read timeout not applied: {}", e);
        }
    }
    apply_tick(&ws, None); // handshake phase: no tick (#242)

    let ws = Arc::new(Mutex::new(ws));
    // Outbound relay: senders hand messages to the reader loop, which flushes
    // them between reads - nobody but the reader ever locks the socket for
    // writing, so frames also cannot interleave (#53).
    let (out_tx, out_rx) = mpsc::channel::<Message>();

    let mut hello = String::from("{\"type\":\"hello\",\"proto\":1");
    if let Some(s) = subdomain {
        hello.push_str(&format!(",\"subdomain\":\"{}\"", json_escape(s)));
    }
    if let Some(t) = token {
        hello.push_str(&format!(",\"token\":\"{}\"", json_escape(t)));
    }
    if let Some(r) = resume {
        if !r.is_empty() {
            hello.push_str(&format!(",\"resume\":\"{}\"", json_escape(r)));
        }
    }
    hello.push_str(",\"client\":\"etunnel-rust/1.0\"}");
    ws.lock().unwrap().send(Message::Text(hello.into())).ok();

    let mut url = String::new();
    let mut resume_token = String::new();
    loop {
        let msg = ws.lock().unwrap().read().map_err(|e| format!("read welcome: {}", e))?;
        if let Message::Text(t) = msg {
            let t = t.as_str();
            if let Some(u) = extract_str(t, "url") {
                if t.contains("\"type\":\"welcome\"") {
                    url = u;
                    resume_token = extract_str(t, "resumeToken").unwrap_or_default();
                    break;
                }
            }
            if t.contains("\"type\":\"error\"") {
                let code = extract_str(t, "code").unwrap_or_default();
                return Err(format!("etunnel: {}", code));
            }
        }
    }

    let closed = Arc::new(AtomicBool::new(false));
    let ws2 = Arc::clone(&ws);
    let local = local; // Local (single or routed); serve() resolves per request
    let closed2 = Arc::clone(&closed);
    // #243 M3: liveness bookkeeping shared by the reader (updates) and the
    // keepalive thread (checks).
    let last_pong = Arc::new(Mutex::new(std::time::Instant::now()));
    let last_activity = Arc::new(Mutex::new(std::time::Instant::now()));
    // Welcome received: arm the read tick for the steady-state reader (#242).
    // It handles WouldBlock/TimedOut by design (drops the lock, flushes), so
    // the #53 anti-head-of-line guarantee is preserved from here on.
    apply_tick(&ws2.lock().unwrap(), Some(Duration::from_millis(READ_TICK_MS)));
    // The reader thread gets its own clone; the original stays with the
    // Tunnel so close() can enqueue the bye (#243 M8).
    let reader_tx = out_tx.clone();
    let pong_ref = Arc::clone(&last_pong);
    let act_ref = Arc::clone(&last_activity);
    thread::spawn(move || {
        let mut streams: HashMap<u32, StreamBuf> = HashMap::new();
        let mut out_rx = out_rx;
        loop {
            let msg = {
                let mut w = ws2.lock().unwrap_or_else(|p| p.into_inner());
                match w.read() {
                    Ok(m) => Some(m),
                    Err(tungstenite::Error::Io(ref e))
                        if e.kind() == std::io::ErrorKind::WouldBlock
                            || e.kind() == std::io::ErrorKind::TimedOut => {
                        // read tick elapsed: drop the lock and flush senders
                        None
                    }
                    Err(_) => break,
                }
            };
            drain_outbox(&ws2, &mut out_rx);
            let Some(msg) = msg else { continue };
            *act_ref.lock().unwrap_or_else(|p| p.into_inner()) = std::time::Instant::now(); // #243 M3
            match msg {
                Message::Text(t) => {
                    let t = t.as_str().to_string();
                    if t.contains("\"type\":\"req\"") {
                        let sid = extract_num(&t, "streamId").unwrap_or(0.0) as u32;
                        // #243 M2: refuse a replayed live id or a stream beyond
                        // the cap (Go registerClientStream parity) with an RST.
                        if streams.contains_key(&sid) || streams.len() >= MAX_STREAMS {
                            reader_tx.send(frame(0x03, sid, b"")).ok();
                        } else {
                            streams.insert(sid, StreamBuf {
                                method: extract_str(&t, "method").unwrap_or_else(|| "GET".into()),
                                path: extract_str(&t, "path").unwrap_or_else(|| "/".into()),
                                headers: extract_headers(&t),
                                body: Vec::new(),
                                size: 0,
                            });
                        }
                    } else if t.contains("\"type\":\"ping\"") {
                        let ts = extract_num(&t, "ts").unwrap_or(0.0) as i64;
                        reader_tx.send(Message::Text(format!("{{\"type\":\"pong\",\"ts\":{}}}", ts).into())).ok();
                    } else if t.contains("\"type\":\"pong\"") {
                        *pong_ref.lock().unwrap_or_else(|p| p.into_inner()) = std::time::Instant::now(); // #243 M3
                    } else if t.contains("\"type\":\"bye\"") {
                        break;
                    }
                }
                Message::Binary(b) => {
                    if b.len() < 9 { drain_outbox(&ws2, &mut out_rx); continue; }
                    let kind = b[0];
                    let sid = u32::from_be_bytes([b[1], b[2], b[3], b[4]]);
                    let n = u32::from_be_bytes([b[5], b[6], b[7], b[8]]) as usize;
                    let payload = &b[9..(9 + n).min(b.len())];
                    let Some(st) = streams.get_mut(&sid) else { continue };
                    match kind {
                        0x01 => {
                            // #243 M2: bound the buffered request body; over
                            // the limit the stream is dropped and refused with
                            // an RST instead of buffering the server into our
                            // RAM.
                            st.size += payload.len();
                            if st.size > MAX_STREAM_BODY {
                                streams.remove(&sid);
                                reader_tx.send(frame(0x03, sid, b"")).ok();
                            } else {
                                st.body.extend_from_slice(payload);
                            }
                        }
                        0x02 => {
                            let st = streams.remove(&sid).unwrap();
                            let tx3 = reader_tx.clone();
                            let local3 = local.clone();
                            thread::spawn(move || serve(tx3, local3, sid, st));
                        }
                        0x03 => { streams.remove(&sid); }
                        _ => {}
                    }
                }
                Message::Close(_) => break,
                _ => {}
            }
        }
        closed2.store(true, Ordering::SeqCst);
    });

    // #243 M3: client keepalive (Go pingLoop parity): ping every 30s through
    // the outbound relay; when neither a pong nor any traffic has been seen
    // for 150s the tunnel is dead (NAT blackhole) - send a Close so the
    // reader tears the socket down and the supervisor reconnects.
    {
        let ping_tx = out_tx.clone();
        let ping_closed = Arc::clone(&closed);
        let pong_ref = Arc::clone(&last_pong);
        let act_ref = Arc::clone(&last_activity);
        thread::spawn(move || loop {
            thread::sleep(Duration::from_secs(PING_INTERVAL_SECS));
            if ping_closed.load(Ordering::SeqCst) {
                return;
            }
            let ts = std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .map(|d| d.as_secs())
                .unwrap_or(0);
            if ping_tx
                .send(Message::Text(format!("{{\"type\":\"ping\",\"ts\":{}}}", ts).into()))
                .is_err()
            {
                return; // reader gone: nothing left to keep alive
            }
            let now = std::time::Instant::now();
            let pong_idle = now
                .duration_since(*pong_ref.lock().unwrap_or_else(|p| p.into_inner()))
                > Duration::from_secs(PING_DEADLINE_SECS);
            let act_idle = now
                .duration_since(*act_ref.lock().unwrap_or_else(|p| p.into_inner()))
                > Duration::from_secs(PING_DEADLINE_SECS);
            if pong_idle && act_idle {
                eprintln!("etunnel: ping timeout");
                ping_tx.send(Message::Close(None)).ok();
                ping_closed.store(true, Ordering::SeqCst);
                return;
            }
        });
    }

    Ok(Tunnel { url, resume_token, closed, out: Some(out_tx) })
}

/// Reader-side flush of the outbound relay: sends every queued message on the
/// socket the reader owns (#53).
fn drain_outbox(ws: &Mutex<tungstenite::WebSocket<MaybeTlsStream<TcpStream>>>,
                rx: &mut mpsc::Receiver<Message>) {
    while let Ok(m) = rx.try_recv() {
        let mut w = ws.lock().unwrap_or_else(|p| p.into_inner());
        if w.send(m).is_err() {
            return;
        }
    }
}

/// Themed 502 page shown when the local app is down (same HTML as the other SDKs).
const ERR_PAGE_A: &str = r##"<!doctype html><html><head><meta charset="utf-8">
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
<div class="k">error</div><div class="v err">"##;

const ERR_PAGE_B: &str = r##"</div>
</div>
<div class="tip"><span class="pulse"></span><b>Fix:</b> start your local service and reload -
the tunnel stays connected and keeps retrying for you.</div>
<footer>powered by <a href="https://github.com/topcheer/etunnel">etunnel</a></footer>
</main></body></html>"##;

fn escape_html(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        match c {
            '&' => out.push_str("&amp;"),
            '<' => out.push_str("&lt;"),
            '>' => out.push_str("&gt;"),
            _ => out.push(c),
        }
    }
    out
}

fn local_error_page(msg: &str) -> String {
    format!("{}{}{}", ERR_PAGE_A, escape_html(msg), ERR_PAGE_B)
}

mod route;

use route::{parse_conf, parse_route_spec, Route};

/// Local dial target: one address (legacy) or a route table (multi-target).
#[derive(Clone)]
enum Local {
    One(String),
    Routes(Vec<Route>),
}

impl Local {
    fn resolve(&self, path: &str) -> Option<(String, String)> {
        match self {
            Local::One(a) => Some((a.clone(), path.to_string())),
            Local::Routes(rs) => route::route_for(rs, path),
        }
    }
}

fn serve(tx: mpsc::Sender<Message>, local: Local, sid: u32, st: StreamBuf) {
    let (addr, path) = match local.resolve(&st.path) {
        Some(hit) => hit,
        None => {
            // No route matches: a real 404 answered at the SDK edge.
            tx.send(Message::Text(format!(
                "{{\"type\":\"res\",\"streamId\":{},\"status\":404,\"headers\":{{\"Content-Type\":[\"text/plain; charset=utf-8\"]}}}}", sid).into())).ok();
            tx.send(frame(0x01, sid, b"404 page not found (no route matches this path)\n")).ok();
            tx.send(frame(0x02, sid, b"")).ok();
            return;
        }
    };
    let hop = |k: &str| {
        matches!(k.to_ascii_lowercase().as_str(), "connection" | "transfer-encoding" | "keep-alive"
            | "upgrade" | "content-length" | "host" | "proxy-connection" | "te" | "trailer")
    };
    let mut req = ureq::request(&st.method, &format!("http://{}{}", addr, path));
    for (k, vs) in &st.headers {
        if hop(k) { continue; }
        for v in vs {
            req = req.set(k, v);
        }
    }
    let body = st.body.clone();
    let resp = if st.method == "GET" || st.method == "HEAD" {
        req.call()
    } else {
        req.send_bytes(&body)
    };

    // #243 M7: bodies are buffered; a response over the cap must not end
    // with a clean FIN that fakes a complete 200 - detect and RST instead.
    const BODY_CAP: usize = 64 * 1024 * 1024;
    let mut truncated = false;
    let (status, headers, out) = match resp {
        Ok(r) => {
            let status = r.status();
            let mut hdrs: Vec<(String, String)> = Vec::new();
            // #243 M6: all(name) returns every value - header(name)
            // returns only the first, losing repeated headers (Set-Cookie).
            for name in r.headers_names() {
                if hop(&name) { continue; }
                for v in r.all(&name) {
                    hdrs.push((name.clone(), v.to_string()));
                }
            }
            let mut buf = Vec::new();
            let _ = r.into_reader().take(BODY_CAP as u64 + 1).read_to_end(&mut buf);
            if buf.len() > BODY_CAP {
                buf.truncate(BODY_CAP);
                truncated = true;
            }
            (status, hdrs, buf)
        }
        Err(ureq::Error::Status(code, r)) => {
            // #243 M6: error responses keep their headers (they were
            // dropped entirely before) and get the same full-value scan.
            let mut hdrs: Vec<(String, String)> = Vec::new();
            for name in r.headers_names() {
                if hop(&name) { continue; }
                for v in r.all(&name) {
                    hdrs.push((name.clone(), v.to_string()));
                }
            }
            let mut buf = Vec::new();
            let _ = r.into_reader().take(BODY_CAP as u64 + 1).read_to_end(&mut buf);
            if buf.len() > BODY_CAP {
                buf.truncate(BODY_CAP);
                truncated = true;
            }
            (code, hdrs, buf)
        }
        Err(e) => {
            tx.send(Message::Text(format!(
                "{{\"type\":\"res\",\"streamId\":{},\"status\":502,\"headers\":{{\"Content-Type\":[\"text/html; charset=utf-8\"]}}}}", sid).into())).ok();
            tx.send(frame(0x01, sid, local_error_page(&e.to_string()).as_bytes())).ok();
            tx.send(frame(0x02, sid, b"")).ok();
            return;
        }
    };

    let hdr_json = headers
        .iter()
        .map(|(k, v)| format!("\"{}\":[\"{}\"]", json_escape(k), json_escape(v)))
        .collect::<Vec<_>>()
        .join(",");
    tx.send(Message::Text(format!(
        "{{\"type\":\"res\",\"streamId\":{},\"status\":{},\"headers\":{{{}}}}}", sid, status, hdr_json).into())).ok();
    for chunk in out.chunks(MAXP) {
        tx.send(frame(0x01, sid, chunk)).ok();
    }
    if truncated {
        // #243 M7: the visitor must see a reset, not a "complete" 200 with
        // a missing tail.
        eprintln!("etunnel: stream {}: response exceeded the 64MiB buffer, sending RST", sid);
        tx.send(frame(0x03, sid, b"")).ok();
    } else {
        tx.send(frame(0x02, sid, b"")).ok();
    }
}

fn backoff_ms(attempt: u32) -> u64 {
    // 500ms, 1s, 2s, ... capped at 30s, then ±20% jitter. The jitter mixes in
    // the RandomState seed instead of subsec_nanos alone: nanos-derived
    // jitter is predictable across processes started in the same millisecond
    // (#53 LOW).
    let base = (500u64 << attempt.min(6)).min(30_000);
    let r = RandomState::new().build_hasher().finish();
    base * (800 + r % 401) / 1000
}

fn main() {
    // Args: [options] [local-port-or-addr]. Options (all optional):
    //   --route PREFIX=ADDR[,strip]   repeatable; switches to multi-target
    //   --config PATH                 key=value config with [route] blocks
    //   --server URL / --token KEY / --subdomain NAME
    // Precedence: flags > env (ETUNNEL_*) > --config file > defaults.
    let mut server_arg: Option<String> = None;
    let mut token_arg: Option<String> = None;
    let mut subdomain_arg: Option<String> = None;
    let mut route_specs: Vec<String> = Vec::new();
    let mut conf_path: Option<String> = None;
    let mut positional: Option<String> = None;
    let mut args = std::env::args().skip(1);
    while let Some(a) = args.next() {
        match a.as_str() {
            "--route" => route_specs.push(args.next().unwrap_or_else(|| { eprintln!("etunnel: --route needs PREFIX=ADDR[,strip]"); std::process::exit(2); })),
            "--config" => conf_path = Some(args.next().unwrap_or_else(|| { eprintln!("etunnel: --config needs a path"); std::process::exit(2); })),
            "--server" => server_arg = Some(args.next().unwrap_or_else(|| { eprintln!("etunnel: --server needs a URL"); std::process::exit(2); })),
            "--token" => token_arg = Some(args.next().unwrap_or_else(|| { eprintln!("etunnel: --token needs a key"); std::process::exit(2); })),
            "--subdomain" => subdomain_arg = Some(args.next().unwrap_or_else(|| { eprintln!("etunnel: --subdomain needs a name"); std::process::exit(2); })),
            "--help" | "-h" => {
                eprintln!("usage: etunnel [--server URL] [--token KEY] [--subdomain NAME] [local-port-or-addr]\n       etunnel [--config etunnel.conf] [--route PREFIX=ADDR[,strip]]...");
                std::process::exit(0);
            }
            other if other.starts_with("--") => { eprintln!("etunnel: unknown flag {}", other); std::process::exit(2); }
            other => positional = Some(other.to_string()),
        }
    }
    let conf = {
        let path = conf_path.clone().or_else(|| std::path::Path::new("etunnel.conf").exists().then(|| "etunnel.conf".to_string()));
        match path {
            Some(p) => match std::fs::read_to_string(&p) {
                Ok(text) => match parse_conf(&text) {
                    Ok(c) => c,
                    Err(e) => { eprintln!("etunnel: {}: {}", p, e); std::process::exit(2); }
                },
                Err(e) => { eprintln!("etunnel: read {}: {}", p, e); std::process::exit(2); }
            },
            None => parse_conf("").unwrap(),
        }
    };
    let server = server_arg
        .or_else(|| std::env::var("ETUNNEL_SERVER").ok())
        .or(conf.server)
        .unwrap_or_else(|| option_env!("ETUNNEL_BUILD_SERVER").unwrap_or("wss://t.etun.uk/tunnel").to_string());
    let token = token_arg
        .or_else(|| std::env::var("ETUNNEL_TOKEN").ok())
        .or(conf.token);
    let subdomain = subdomain_arg
        .or_else(|| std::env::var("ETUNNEL_SUBDOMAIN").ok())
        .or(conf.subdomain);
    // CLI --route specs win over config [route] blocks; a positional/local
    // only applies in single-target mode.
    let cli_routes: Result<Vec<Route>, String> = route_specs.iter()
        .map(|s| parse_route_spec(s)).collect();
    let cli_routes = match cli_routes {
        Ok(rs) => rs,
        Err(e) => { eprintln!("etunnel: {}", e); std::process::exit(2); }
    };
    let (local, routes) = if !cli_routes.is_empty() {
        if positional.is_some() { eprintln!("etunnel: positional <local-port-or-addr> cannot be combined with --route"); std::process::exit(2); }
        (None, cli_routes)
    } else if !conf.routes.is_empty() {
        (None, conf.routes)
    } else {
        let l = positional
            .or_else(|| std::env::var("ETUNNEL_LOCAL").ok())
            .unwrap_or_else(|| "3000".into());
        let l = if l.contains(':') { l } else { format!("127.0.0.1:{}", l) };
        let (host, port) = l.rsplit_once(':').unwrap();
        let port: u16 = port.parse().unwrap_or(3000);
        (Some(format!("{}:{}", host, port)), Vec::new())
    };
    if local.is_none() && routes.is_empty() {
        eprintln!("etunnel: nothing to expose (no --route, no config [route], no local address)");
        std::process::exit(2);
    }
    // ETUNNEL_RECONNECT=0 restores the one-shot behaviour (exit on first error).
    let reconnect = std::env::var("ETUNNEL_RECONNECT").map(|v| v != "0").unwrap_or(true);

    // SIGINT/SIGTERM keep their default disposition: the process dies at once,
    // so a stopped CLI never reconnects.
    let mut attempt: u32 = 0;
    // Carried across drops so a reconnect reclaims the same subdomain.
    let mut resume: Option<String> = None;
    loop {
        let connected = if !routes.is_empty() {
            connect_routes(&server, routes.clone(), token.as_deref(), subdomain.as_deref(), resume.as_deref())
        } else {
            connect_full_resume(&server, local.as_deref().unwrap(), token.as_deref(), subdomain.as_deref(), resume.as_deref())
        };
        match connected {
            Ok(t) => {
                println!("TUNNEL_URL: {}", t.url);
                attempt = 0;
                if !t.resume_token.is_empty() {
                    resume = Some(t.resume_token.clone());
                }
                if !reconnect {
                    // #243 L4: without reconnect there is nothing to wait
                    // for - exit instead of sleeping forever.
                    std::process::exit(0);
                }
                while !t.closed.load(Ordering::SeqCst) {
                    thread::sleep(std::time::Duration::from_millis(200));
                }
                eprintln!("etunnel: connection lost, reconnecting");
            }
            Err(e) => {
                eprintln!("etunnel: {}", e);
                if !reconnect {
                    std::process::exit(1);
                }
            }
        }
        let delay = backoff_ms(attempt);
        attempt = attempt.saturating_add(1);
        eprintln!("etunnel: reconnect in {}ms", delay);
        thread::sleep(std::time::Duration::from_millis(delay));
    }
}

// --- CLI loopback login (RFC 8252 style; server /auth/cli/* endpoints) ---
//   let creds = etunnel_login("https://t.etun.uk", None, 600).ok();
// Binds a random 127.0.0.1 port, opens the browser (prints the URL when the
// opener fails), waits for the one-time code (10-minute budget), exchanges
// it. Persist creds and pass it as the token option on future connects.
pub struct LoginCredentials {
    pub api_token: String,
    pub subdomains: Vec<String>,
    pub plan: String,
}

pub fn etunnel_login<F: Fn(&str) -> std::io::Result<()>>(
    server: &str,
    open_browser: Option<F>,
    timeout_secs: u64,
) -> Result<LoginCredentials, String> {
    use std::io::{Read, Write};
    let base = server.trim_end_matches('/');
    if base.is_empty() {
        return Err("etunnel: login: server required".into());
    }

    let srv = std::net::TcpListener::bind("127.0.0.1:0").map_err(|e| e.to_string())?;
    let port = srv.local_addr().map_err(|e| e.to_string())?.port();

    let start_body = format!("{{\"redirect_uri\":\"http://127.0.0.1:{port}/callback\"}}");
    let start = ureq::post(&format!("{base}/auth/cli/start"))
        .set("Content-Type", "application/json")
        .send_string(&start_body)
        .map_err(|e| format!("etunnel: login start: {e}"))?
        .into_string().map_err(|e| e.to_string())?;
    let auth_url = json_field(&start, "auth_url").ok_or("etunnel: login: empty start payload")?;
    let device_code = json_field(&start, "device_code").ok_or("etunnel: login: empty start payload")?;
    let mut opened = false;
    if let Some(opener) = &open_browser {
        if opener(&auth_url).is_ok() { opened = true; }
    }
    if !opened {
        let cmd = if cfg!(target_os = "macos") { "open" } else { "xdg-open" };
        if std::process::Command::new(cmd).arg(&auth_url).spawn().is_ok() {
            opened = true;
        }
    }
    if !opened {
        eprintln!("etunnel: authorize in your browser: {auth_url}");
    }

    srv.set_nonblocking(true).ok();
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(timeout_secs);
    let mut code = String::new();
    while code.is_empty() {
        if std::time::Instant::now() > deadline {
            return Err("etunnel: login timed out".into());
        }
        let (mut c, _) = match srv.accept() {
            Ok(x) => x,
            Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => {
                std::thread::sleep(std::time::Duration::from_millis(100));
                continue;
            }
            Err(e) => return Err(e.to_string()),
        };
        let mut req = String::new();
        let _ = c.read_to_string(&mut req);
        if let Some(q) = req.split(' ').nth(1) {
            if let Some(kv) = q.split('?').nth(1) {
                for pair in kv.split('&') {
                    if let Some(v) = pair.strip_prefix("code=") {
                        code = v.to_string();
                    }
                }
            }
        }
        let body = "<!doctype html><p>Authorized. You can close this tab and return to the terminal.</p>";
        let _ = c.write_all(format!(
            "HTTP/1.0 200 OK\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
            body.len(), body).as_bytes());
    }
    drop(srv);

    let ex_body = format!("{{\"device_code\":\"{device_code}\",\"code\":\"{code}\"}}");
    let ex = ureq::post(&format!("{base}/auth/cli/exchange"))
        .set("Content-Type", "application/json")
        .send_string(&ex_body)
        .map_err(|e| format!("etunnel: login exchange: {e}"))?
        .into_string().map_err(|e| e.to_string())?;
    let api_token = json_field(&ex, "api_token").filter(|t| !t.is_empty())
        .ok_or("etunnel: login: no api_token in exchange response")?;
    let subdomains = json_string_array(&ex, "subdomains").unwrap_or_default();
    let plan = json_field(&ex, "plan").unwrap_or_default();
    Ok(LoginCredentials { api_token, subdomains, plan })
}

// Minimal extractors for the flat exchange payload (no JSON crate needed).
fn json_field(s: &str, key: &str) -> Option<String> {
    let pat = format!("\"{key}\":\"");
    let i = s.find(&pat)? + pat.len();
    let rest = &s[i..];
    let mut out = String::new();
    let mut esc = false;
    for ch in rest.chars() {
        if esc { out.push(ch); esc = false; continue; }
        match ch {
            '\\' => esc = true,
            '"' => return Some(out),
            _ => out.push(ch),
        }
    }
    None
}

fn json_string_array(s: &str, key: &str) -> Option<Vec<String>> {
    let pat = format!("\"{key}\":[");
    let i = s.find(&pat)? + pat.len();
    let rest = &s[i..];
    let end = rest.find(']')?;
    let inner = &rest[..end];
    Some(inner.split(',').map(|x| x.trim().trim_matches('"').to_string()).filter(|x| !x.is_empty()).collect())
}

#[cfg(test)]
mod login_e2e {
    use super::*;
    // E2E against sdks/testtools/loginserver (run via tests/test_login.sh,
    // which exports FX_ADDR). Skipped when the fixture is not up.
    #[test]
    fn loopback_login_start_grant_exchange() {
        let addr = match std::env::var("FX_ADDR") {
            Ok(a) if !a.is_empty() => a,
            _ => { eprintln!("SKIP: FX_ADDR not set"); return; }
        };
        let creds = etunnel_login(&format!("http://{addr}"), Some(|u: &str| {
            std::process::Command::new("curl").args(["-s", "-o", "/dev/null", u]).spawn().map(|_| ()).map_err(|e| e).ok();
            Ok(())
        }), 30).expect("login flow failed");
        assert_eq!(creds.api_token, "tok-123");
        assert_eq!(creds.subdomains, vec!["my-sub"]);
        assert_eq!(creds.plan, "pro");
    }
}
