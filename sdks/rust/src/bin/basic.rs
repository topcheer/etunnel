// Minimal etunnel example (Rust).
//   cargo run --example basic -- 3000
#[path = "../main.rs"]
mod etunnel_sdk;

use std::io::{Read, Write};
use std::net::TcpListener;

fn main() -> Result<(), String> {
    let port: u16 = std::env::args().nth(1).and_then(|a| a.parse().ok()).unwrap_or(3000);
    let listener = TcpListener::bind(("127.0.0.1", port)).map_err(|e| e.to_string())?;
    std::thread::spawn(move || {
        for stream in listener.incoming() {
            let mut s = match stream { Ok(s) => s, Err(_) => continue };
            let mut buf = [0u8; 1024];
            let _ = s.read(&mut buf);
            let body = "hello from etunnel-example\n";
            let resp = format!("HTTP/1.1 200 OK\r\ncontent-type: text/plain\r\ncontent-length: {}\r\n\r\n{}", body.len(), body);
            let _ = s.write_all(resp.as_bytes());
        }
    });
    println!("local http://127.0.0.1:{}", port);
    let t = etunnel_sdk::connect("wss://t.etun.uk/tunnel", port, None, None)?;
    println!("public URL: {}", t.url);
    loop { std::thread::sleep(std::time::Duration::from_secs(3600)); }
}
