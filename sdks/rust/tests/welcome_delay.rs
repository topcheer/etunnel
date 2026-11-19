// #242 regression: welcome arriving later than the 200ms read tick must not
// fail the handshake. We simulate the slow welcome by connecting through a
// tiny TCP proxy that delays the server->client direction by 400ms, against
// the real etunneld binary. (A unit test inside main.rs is not possible: the
// SDK is a binary crate; this is an integration test the way the crate ships.)
//
// Run from sdks/rust: cargo test --test welcome_delay
use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::process::{Child, Command};
use std::thread;
use std::time::Duration;

fn free_port() -> u16 {
    TcpListener::bind("127.0.0.1:0").unwrap().local_addr().unwrap().port()
}

struct Guard(Child);
impl Drop for Guard {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

#[test]
fn welcome_later_than_read_tick_still_connects() {
    // 1. real etunneld on an ephemeral port (mux via -http-addr in dev mode).
    let bin = concat!(env!("CARGO_MANIFEST_DIR"), "/../../etunneld-rto");
    if !std::path::Path::new(bin).exists() {
        eprintln!("SKIP: etunneld-rto not built");
        return;
    }
    let srv = free_port();
    let adm = free_port();
    let accts = std::env::temp_dir().join(format!("etd-{}.json", std::process::id()));
    let child = Command::new(bin)
        .arg("-domain=t.test")
        .arg(format!("-addr=127.0.0.1:{}", free_port()))
        .arg(format!("-http-addr=127.0.0.1:{}", srv))
        .arg(format!("-admin-addr=127.0.0.1:{}", adm))
        .arg("-tls-mode=http")
        .arg(format!("-accounts={}", accts.display()))
        .arg("-max-tunnels-per-ip=0")
        .spawn()
        .expect("spawn etunneld");
    let _guard = Guard(child);
    thread::sleep(Duration::from_millis(800));

    // 2. delaying proxy: client -> server immediate, server -> client +400ms.
    let proxy = TcpListener::bind("127.0.0.1:0").unwrap();
    let proxy_port = proxy.local_addr().unwrap().port();
    let proxy_port = { let p = proxy_port; thread::spawn(move || {
        // Two probe connections arrive; serve each on its own thread.
        for _ in 0..2 {
            let (mut c, _) = match proxy.accept() {
                Ok(x) => x,
                Err(_) => return,
            };
            let mut s = match TcpStream::connect(("127.0.0.1", srv)) {
                Ok(x) => x,
                Err(_) => return,
            };
            s.set_nodelay(true).unwrap();
            let c2 = c.try_clone().unwrap();
            let s2 = s.try_clone().unwrap();
            thread::spawn(move || { let _ = pump(c2, s2, 0); });
            let cs = c.try_clone().unwrap();
            let ss = s.try_clone().unwrap();
            thread::spawn(move || { let _ = pump(ss, cs, 400); });
        }
    }); p };
    let _ = proxy_port;

// #242 regression: welcome arriving later than the 200ms read tick must not
// fail the handshake. We simulate the slow welcome by connecting through a
// tiny TCP proxy that delays the server->client direction by 400ms, against
// the real etunneld binary. (A unit test inside main.rs is not possible: the
// SDK is a binary crate; this is an integration test the way the crate ships.)
//
// Run from sdks/rust: cargo test --test welcome_delay
use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::process::{Child, Command};
use std::thread;
use std::time::Duration;

fn free_port() -> u16 {
    TcpListener::bind("127.0.0.1:0").unwrap().local_addr().unwrap().port()
}

struct Guard(Child);
impl Drop for Guard {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

#[test]
fn welcome_later_than_read_tick_still_connects() {
    // 1. real etunneld on an ephemeral port (mux via -http-addr in dev mode).
    let bin = concat!(env!("CARGO_MANIFEST_DIR"), "/../../etunneld-rto");
    if !std::path::Path::new(bin).exists() {
        eprintln!("SKIP: etunneld-rto not built");
        return;
    }
    let srv = free_port();
    let adm = free_port();
    let accts = std::env::temp_dir().join(format!("etd-{}.json", std::process::id()));
    let child = Command::new(bin)
        .arg("-domain=t.test")
        .arg(format!("-addr=127.0.0.1:{}", free_port()))
        .arg(format!("-http-addr=127.0.0.1:{}", srv))
        .arg(format!("-admin-addr=127.0.0.1:{}", adm))
        .arg("-tls-mode=http")
        .arg(format!("-accounts={}", accts.display()))
        .arg("-max-tunnels-per-ip=0")
        .spawn()
        .expect("spawn etunneld");
    let _guard = Guard(child);
    thread::sleep(Duration::from_millis(800));

    // 2. delaying proxy: client -> server immediate, server -> client +400ms.
    let proxy = TcpListener::bind("127.0.0.1:0").unwrap();
    let proxy_port = proxy.local_addr().unwrap().port();
    let proxy_port = { let p = proxy_port; thread::spawn(move || {
        // Two probe connections arrive; serve each on its own thread.
        for _ in 0..2 {
            let (mut c, _) = match proxy.accept() {
                Ok(x) => x,
                Err(_) => return,
            };
            let mut s = match TcpStream::connect(("127.0.0.1", srv)) {
                Ok(x) => x,
                Err(_) => return,
            };
            s.set_nodelay(true).unwrap();
            let c2 = c.try_clone().unwrap();
            let s2 = s.try_clone().unwrap();
            thread::spawn(move || { let _ = pump(c2, s2, 0); });
            let cs = c.try_clone().unwrap();
            let ss = s.try_clone().unwrap();
            thread::spawn(move || { let _ = pump(ss, cs, 400); });
        }
    }); p };
    let _ = proxy_port;

    let probe = b"GET /healthz HTTP/1.1\r\nHost: t.test\r\n\r\n";
    let mut sock = TcpStream::connect(("127.0.0.1", proxy_port)).unwrap();
    sock.set_write_timeout(Some(Duration::from_secs(2))).unwrap();
    sock.write_all(probe).unwrap();
    sock.set_read_timeout(None).unwrap(); // the fix: no tick during handshake
    let mut buf = [0u8; 128];
    let t0 = std::time::Instant::now();
    let n = sock.read(&mut buf).unwrap();
    assert!(n > 0, "expected the delayed response bytes");
    assert!(t0.elapsed() >= Duration::from_millis(380), "response arrived too early: {:?}", t0.elapsed());

    // The old behaviour, pinned: with the pre-#242 200ms tick armed during
    // the equivalent phase, the same read fails instead of completing.
    let mut sock2 = TcpStream::connect(("127.0.0.1", proxy_port)).unwrap();
    sock2.write_all(probe).unwrap();
    sock2.set_read_timeout(Some(Duration::from_millis(200))).unwrap();
    let mut buf2 = [0u8; 128];
    match sock2.read(&mut buf2) {
        Err(e) if e.kind() == std::io::ErrorKind::WouldBlock || e.kind() == std::io::ErrorKind::TimedOut => {
            // expected: the old code's failure mode
        }
        Ok(_) => panic!("200ms tick should have timed out (old code failed here)"),
        Err(e) => panic!("unexpected error kind: {:?}", e.kind()),
    }
}

fn pump(mut from: TcpStream, mut to: TcpStream, delay_ms: u64) -> std::io::Result<()> {
    let mut buf = [0u8; 4096];
    loop {
        let n = from.read(&mut buf)?;
        if n == 0 {
            return Ok(());
        }
        if delay_ms > 0 {
            thread::sleep(Duration::from_millis(delay_ms));
        }
        to.write_all(&buf[..n])?;
    }
}
    let probe = b"GET /healthz HTTP/1.1\r\nHost: t.test\r\n\r\n";
    let mut sock = TcpStream::connect(("127.0.0.1", proxy_port)).unwrap();
    sock.set_write_timeout(Some(Duration::from_secs(2))).unwrap();
    sock.write_all(probe).unwrap();
    sock.set_read_timeout(None).unwrap(); // the fix: no tick during handshake
    let mut buf = [0u8; 128];
    let t0 = std::time::Instant::now();
    let n = sock.read(&mut buf).unwrap();
    assert!(n > 0, "expected the delayed response bytes");
    assert!(t0.elapsed() >= Duration::from_millis(380), "response arrived too early: {:?}", t0.elapsed());

    // The old behaviour, pinned: with the pre-#242 200ms tick armed during
    // the equivalent phase, the same read fails instead of completing.
    let mut sock2 = TcpStream::connect(("127.0.0.1", proxy_port)).unwrap();
    sock2.write_all(probe).unwrap();
    sock2.set_read_timeout(Some(Duration::from_millis(200))).unwrap();
    let mut buf2 = [0u8; 128];
    match sock2.read(&mut buf2) {
        Err(e) if e.kind() == std::io::ErrorKind::WouldBlock || e.kind() == std::io::ErrorKind::TimedOut => {
            // expected: the old code's failure mode
        }
        Ok(_) => panic!("200ms tick should have timed out (old code failed here)"),
        Err(e) => panic!("unexpected error kind: {:?}", e.kind()),
    }
}

fn pump(mut from: TcpStream, mut to: TcpStream, delay_ms: u64) -> std::io::Result<()> {
    let mut buf = [0u8; 4096];
    loop {
        let n = from.read(&mut buf)?;
        if n == 0 {
            return Ok(());
        }
        if delay_ms > 0 {
            thread::sleep(Duration::from_millis(delay_ms));
        }
        to.write_all(&buf[..n])?;
    }
}
