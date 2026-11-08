// etunnel SDK for Rust (tungstenite + ureq).
// Buffer-based protocol client (bodies buffered per stream; no WS upgrades).
use std::collections::HashMap;
use std::io::Read;
use std::net::TcpStream;
use std::sync::{Arc, Mutex};
use std::thread;

use tungstenite::client::IntoClientRequest;
use tungstenite::handshake::client::Response;
use tungstenite::protocol::{Message, WebSocketConfig};
use tungstenite::stream::MaybeTlsStream;

const MAXP: usize = 32768;

pub struct Options {
    pub token: Option<String>,
    pub subdomain: Option<String>,
}

pub struct Tunnel {
    pub url: String,
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
    // walk pairs until the matching close brace (naive but messages are flat)
    let bytes = body.as_bytes();
    let mut i = 0usize;
    let mut depth = 1usize;
    let mut cur_key = String::new();
    let mut cur_vals: Vec<String> = Vec::new();
    let mut in_key = false;
    let mut in_val = false;
    let mut buf = String::new();
    while i < bytes.len() {
        let c = bytes[i] as char;
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

    let ws = Arc::new(Mutex::new(ws));

    let mut hello = String::from("{\"type\":\"hello\",\"proto\":1");
    if let Some(s) = subdomain {
        hello.push_str(&format!(",\"subdomain\":\"{}\"", json_escape(s)));
    }
    if let Some(t) = token {
        hello.push_str(&format!(",\"token\":\"{}\"", json_escape(t)));
    }
    hello.push_str(",\"client\":\"etunnel-rust/1.0\"}");
    ws.lock().unwrap().send(Message::Text(hello.into())).ok();

    let mut url = String::new();
    loop {
        let msg = ws.lock().unwrap().read().map_err(|e| format!("read welcome: {}", e))?;
        if let Message::Text(t) = msg {
            let t = t.as_str();
            if let Some(u) = extract_str(t, "url") {
                if t.contains("\"type\":\"welcome\"") {
                    url = u;
                    break;
                }
            }
            if t.contains("\"type\":\"error\"") {
                let code = extract_str(t, "code").unwrap_or_default();
                return Err(format!("etunnel: {}", code));
            }
        }
    }

    let ws2 = Arc::clone(&ws);
    let local = local_addr.to_string();
    thread::spawn(move || {
        let mut streams: HashMap<u32, StreamBuf> = HashMap::new();
        loop {
            let msg = {
                let mut w = ws2.lock().unwrap();
                match w.read() {
                    Ok(m) => m,
                    Err(_) => return,
                }
            };
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
                        let mut w = ws2.lock().unwrap();
                        w.send(Message::Text(format!("{{\"type\":\"pong\",\"ts\":{}}}", ts).into())).ok();
                    } else if t.contains("\"type\":\"bye\"") {
                        return;
                    }
                }
                Message::Binary(b) => {
                    if b.len() < 9 { continue; }
                    let kind = b[0];
                    let sid = u32::from_be_bytes([b[1], b[2], b[3], b[4]]);
                    let n = u32::from_be_bytes([b[5], b[6], b[7], b[8]]) as usize;
                    let payload = &b[9..(9 + n).min(b.len())];
                    let Some(st) = streams.get_mut(&sid) else { continue };
                    match kind {
                        0x01 => st.body.extend_from_slice(payload),
                        0x02 => {
                            let st = streams.remove(&sid).unwrap();
                            let ws3 = Arc::clone(&ws2);
                            let local3 = local.clone();
                            thread::spawn(move || serve(ws3, local3, sid, st));
                        }
                        0x03 => { streams.remove(&sid); }
                        _ => {}
                    }
                }
                Message::Close(_) => return,
                _ => {}
            }
        }
    });

    Ok(Tunnel { url })
}

fn serve(ws: Arc<Mutex<tungstenite::WebSocket<MaybeTlsStream<TcpStream>>>>, local: String, sid: u32, st: StreamBuf) {
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
            let mut w = ws.lock().unwrap();
            w.send(Message::Text(format!(
                "{{\"type\":\"res\",\"streamId\":{},\"status\":502,\"headers\":{{\"Content-Type\":[\"text/plain\"]}}}}", sid).into())).ok();
            w.send(frame(0x01, sid, format!("etunnel: local service error: {}\n", e).as_bytes())).ok();
            w.send(frame(0x02, sid, b"")).ok();
            return;
        }
    };

    let hdr_json = headers
        .iter()
        .map(|(k, v)| format!("\"{}\":[\"{}\"]", json_escape(k), json_escape(v)))
        .collect::<Vec<_>>()
        .join(",");
    let mut w = ws.lock().unwrap();
    w.send(Message::Text(format!(
        "{{\"type\":\"res\",\"streamId\":{},\"status\":{},\"headers\":{{{}}}}}", sid, status, hdr_json).into())).ok();
    for chunk in out.chunks(MAXP) {
        w.send(frame(0x01, sid, chunk)).ok();
    }
    w.send(frame(0x02, sid, b"")).ok();
}

fn main() {
    let server = std::env::var("ETUNNEL_SERVER").unwrap_or_else(|_| "wss://t.ggcode.uk/tunnel".into());
    let local = std::env::args().nth(1)
        .or_else(|| std::env::var("ETUNNEL_LOCAL").ok())
        .unwrap_or_else(|| "3000".into());
    let local = if local.contains(':') { local } else { format!("127.0.0.1:{}", local) };
    let (host, port) = local.rsplit_once(':').unwrap();
    let port: u16 = port.parse().unwrap_or(3000);
    let local = format!("{}:{}", host, port);
    match connect_full(&server, &local,
        std::env::var("ETUNNEL_TOKEN").ok().as_deref(),
        std::env::var("ETUNNEL_SUBDOMAIN").ok().as_deref()) {
        Ok(t) => {
            println!("TUNNEL_URL: {}", t.url);
            loop { thread::sleep(std::time::Duration::from_secs(3600)); }
        }
        Err(e) => {
            eprintln!("etunnel: {}", e);
            std::process::exit(1);
        }
    }
}
