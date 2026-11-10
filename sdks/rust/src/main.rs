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
}

impl Tunnel {
    /// Dropping the tunnel process / task ends the connection; the server
    /// detects the closed socket and frees the subdomain.
    pub fn close(&self) {}
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
}

pub fn connect(server: &str, local_port: u16, token: Option<&str>, subdomain: Option<&str>) -> Result<Tunnel, String> {
    connect_full(server, &format!("127.0.0.1:{}", local_port), token, subdomain)
}

pub fn connect_full(server: &str, local_addr: &str, token: Option<&str>, subdomain: Option<&str>) -> Result<Tunnel, String> {
    connect_full_resume(server, local_addr, token, subdomain, None)
}

/// Like [`connect_full`], plus an optional resume token from a previous
/// session so the server hands back the same subdomain after a drop.
pub fn connect_full_resume(server: &str, local_addr: &str, token: Option<&str>, subdomain: Option<&str>, resume: Option<&str>) -> Result<Tunnel, String> {
    let mut request = server
        .into_client_request()
        .map_err(|e| format!("bad url: {}", e))?;
    request.headers_mut().insert(
        "Sec-WebSocket-Protocol",
        "etunnel-v1".parse().unwrap(),
    );
    let cfg = WebSocketConfig::default();

    let _ = cfg;
    let (ws, _resp) = tungstenite::connect(request)
        .map_err(|e| format!("connect: {}", e))?;

    // Bound every blocking read so the reader periodically releases the
    // socket lock and drains the outbound queue: holding the lock across an
    // unbounded read() blocked every sender (serve threads, pong) until the
    // next inbound frame arrived (#53 MED). MaybeTlsStream has no blanket
    // timeout setter, so drill into the concrete stream it wraps.
    {
        let tick = Some(Duration::from_millis(READ_TICK_MS));
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
    let local = local_addr.to_string();
    let closed2 = Arc::clone(&closed);
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
            match msg {
                Message::Text(t) => {
                    let t = t.as_str().to_string();
                    if t.contains("\"type\":\"req\"") {
                        let sid = extract_num(&t, "streamId").unwrap_or(0.0) as u32;
                        streams.insert(sid, StreamBuf {
                            method: extract_str(&t, "method").unwrap_or_else(|| "GET".into()),
                            path: extract_str(&t, "path").unwrap_or_else(|| "/".into()),
                            headers: extract_headers(&t),
                            body: Vec::new(),
                        });
                    } else if t.contains("\"type\":\"ping\"") {
                        let ts = extract_num(&t, "ts").unwrap_or(0.0) as i64;
                        out_tx.send(Message::Text(format!("{{\"type\":\"pong\",\"ts\":{}}}", ts).into())).ok();
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
                        0x01 => st.body.extend_from_slice(payload),
                        0x02 => {
                            let st = streams.remove(&sid).unwrap();
                            let tx3 = out_tx.clone();
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

    Ok(Tunnel { url, resume_token, closed })
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

fn serve(tx: mpsc::Sender<Message>, local: String, sid: u32, st: StreamBuf) {
    let hop = |k: &str| {
        matches!(k.to_ascii_lowercase().as_str(), "connection" | "transfer-encoding" | "keep-alive"
            | "upgrade" | "content-length" | "host" | "proxy-connection" | "te" | "trailer")
    };
    let mut req = ureq::request(&st.method, &format!("http://{}{}", local, st.path));
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

    let (status, headers, out) = match resp {
        Ok(r) => {
            let status = r.status();
            let mut hdrs: Vec<(String, String)> = Vec::new();
            for name in r.headers_names() {
                if hop(&name) { continue; }
                if let Some(v) = r.header(&name) {
                    hdrs.push((name, v.to_string()));
                }
            }
            let mut buf = Vec::new();
            r.into_reader().take(64 * 1024 * 1024).read_to_end(&mut buf).unwrap_or(0);
            (status, hdrs, buf)
        }
        Err(ureq::Error::Status(code, r)) => {
            let mut buf = Vec::new();
            r.into_reader().read_to_end(&mut buf).unwrap_or(0);
            (code, Vec::new(), buf)
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
    tx.send(frame(0x02, sid, b"")).ok();
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
    let server = std::env::var("ETUNNEL_SERVER").unwrap_or_else(|_| option_env!("ETUNNEL_BUILD_SERVER").unwrap_or("wss://t.etun.uk/tunnel").into());
    let local = std::env::args().nth(1)
        .or_else(|| std::env::var("ETUNNEL_LOCAL").ok())
        .unwrap_or_else(|| "3000".into());
    let local = if local.contains(':') { local } else { format!("127.0.0.1:{}", local) };
    let (host, port) = local.rsplit_once(':').unwrap();
    let port: u16 = port.parse().unwrap_or(3000);
    let local = format!("{}:{}", host, port);
    let token = std::env::var("ETUNNEL_TOKEN").ok();
    let subdomain = std::env::var("ETUNNEL_SUBDOMAIN").ok();
    // ETUNNEL_RECONNECT=0 restores the one-shot behaviour (exit on first error).
    let reconnect = std::env::var("ETUNNEL_RECONNECT").map(|v| v != "0").unwrap_or(true);

    // SIGINT/SIGTERM keep their default disposition: the process dies at once,
    // so a stopped CLI never reconnects.
    let mut attempt: u32 = 0;
    // Carried across drops so a reconnect reclaims the same subdomain.
    let mut resume: Option<String> = None;
    loop {
        match connect_full_resume(
            &server,
            &local,
            token.as_deref(),
            subdomain.as_deref(),
            resume.as_deref(),
        ) {
            Ok(t) => {
                println!("TUNNEL_URL: {}", t.url);
                attempt = 0;
                if !t.resume_token.is_empty() {
                    resume = Some(t.resume_token.clone());
                }
                if !reconnect {
                    loop { thread::sleep(std::time::Duration::from_secs(3600)); }
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
