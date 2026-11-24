# etunnel for Rust

[![crates.io](https://img.shields.io/crates/v/etunnel?label=crates.io%20etunnel)](https://crates.io/crates/etunnel)

tungstenite + ureq CLI crate.

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

## Install

```
cargo add etunnel        # library API (1.7.36+)
cargo install etunnel    # CLI binary
```

Requires Rust 1.70+. `cargo install` compiles ~65 crates (about 25 s on an
M-series Mac) and installs a single `etunnel` binary.

## AI agent quick-start (copy this prompt)

````text
Integrate etunnel (Rust SDK) into this project to expose the local service on port <PORT> at a public HTTPS URL.

- Package: `etunnel` on crates.io (library + CLI binary; 1.7.36+) · Install: `cargo add etunnel`
- Server: wss://t.etun.uk/tunnel (override with ETUNNEL_SERVER). Anonymous tier = random subdomain, 4h TTL; custom subdomain / longer TTL need a paid token (opts.token or ETUNNEL_TOKEN).
- Minimal embed (verified against the SDK source):
```rust
use etunnel;
fn main() -> Result<(), Box<dyn std::error::Error>> {
    let t = etunnel::connect_full("wss://t.etun.uk/tunnel", "127.0.0.1:3000", None, None)?;
    println!("TUNNEL_URL: {}", t.url);
    std::thread::sleep(std::time::Duration::from_secs(3600));
    t.close();
    Ok(())
}
```
- Rules: print the public URL as `TUNNEL_URL: <url>` and keep the process alive (the tunnel runs on background threads); call `t.close()` on shutdown to release the subdomain. This SDK is buffer-based (no WebSocket upgrades, no streaming) - fine for webhooks/APIs/dashboards.
- Do not invent other options. Full API/options: #embed and #multi-target-routing. Wire protocol: https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md
````

## CLI

```
etunnel 3000
```

Prints `TUNNEL_URL: https://<subdomain>.t.etun.uk` — that URL now routes to
your local service. Ctrl-C closes the tunnel.

**Reconnects automatically.** If the connection drops (including a server
restart) the CLI re-dials with exponential backoff (500ms → 30s, ±20%
jitter, reset on success) and prints `TUNNEL_URL:` again. The subdomain is
**reclaimed**, not re-randomised: the client replays the server's signed
`resumeToken` in `hello.resume`, so links already shared keep working (the
server needs a stable `SESSION_SECRET`; see PROTOCOL.md §1). Set
`ETUNNEL_RECONNECT=0` to restore the old one-shot behaviour.

## Embed

```rust
use etunnel;

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let t = etunnel::connect_full("wss://t.etun.uk/tunnel", "127.0.0.1:3000", None, None)?;
    println!("TUNNEL_URL: {}", t.url);

    std::thread::sleep(std::time::Duration::from_secs(3600));   // keep the process alive
    t.close();   // sends `bye`, frees the subdomain
    Ok(())
}
```

Library API (1.7.36+): `connect(server, port: u16, token, subdomain)`,
`connect_full(server, "host:port", token, subdomain)`, `connect_full_resume(..., resume)`
and `connect_routes(server, Vec<Route>, token, subdomain, resume)` return
`Result<Tunnel, String>`; `Tunnel { url, resume_token, closed }` with
`close()` and `done()` (channel that fires when the tunnel ends). Reconnection is
the caller's job: replay `t.resume_token` via `connect_full_resume`.

Alternative without a Cargo dependency (also what the CLI is): spawn the
installed binary and relay its `TUNNEL_URL:` lines.

```rust
use std::io::{BufRead, BufReader};
use std::process::{Command, Stdio};

// inside `fn main() -> Result<(), Box<dyn std::error::Error>>`
let mut child = Command::new("etunnel")
    .arg("3000")
    .env("ETUNNEL_SERVER", "wss://t.etun.uk/tunnel")
    .stdout(Stdio::piped())
    .spawn()?;
if let Some(out) = child.stdout.take() {
    for line in BufReader::new(out).lines() {
        println!("{}", line?);
    }
}
child.kill().ok();
Ok(())
```

CLI flags: `etunnel [local-port-or-addr]`,
`--server URL`, `--token KEY`, `--subdomain NAME`, `--route PREFIX=ADDR[,strip]`
(repeatable), `--config PATH`. Precedence: flags > `ETUNNEL_*` env >
`--config` file (`etunnel.conf` in the cwd is picked up automatically) > defaults.

## Environment variables

| Variable | Meaning | Default |
|---|---|---|
| `ETUNNEL_SERVER` | tunnel server WebSocket URL | `wss://t.etun.uk/tunnel` |
| `ETUNNEL_LOCAL` | local port or `host:port` (or 1st CLI arg) | `3000` |
| `ETUNNEL_TOKEN` | paid-plan API key (optional) | - |
| `ETUNNEL_RECONNECT` | `0` disables auto-reconnect (one-shot CLI) | enabled |
| `ETUNNEL_SUBDOMAIN` | custom subdomain (paid plans) | random |

## License

MIT — see [LICENSE](../../LICENSE). Buffer-based protocol client: request and
response bodies are buffered per stream (fine for webhooks/dashboards/demos;
use the [Go SDK](../../sdk) for streaming and WebSocket pass-through).

## Service terms

Use of the etunnel relay service (default `wss://t.etun.uk/tunnel`) is governed
by its [Terms of Service](https://t.etun.uk/terms) and
[Privacy Policy](https://t.etun.uk/privacy).

## Multi-target routing

One public hostname, several local services - the SPA prod-bundle shape
(`/` to the built frontend, `/api` to the backend) without CORS or a
second tunnel. Routing is client-side; the protocol is unchanged.

```rust
use etunnel::{connect_routes, route::Route};

let t = connect_routes(
    "wss://t.example.com/tunnel",
    vec![
        Route { prefix: "/api".into(), addr: "127.0.0.1:8080".into(), strip: true },
        Route { prefix: "/".into(),    addr: "127.0.0.1:3000".into(), strip: false },
    ],
    None, None, None,
)?;
```

Or with the CLI: repeatable `--route /api=127.0.0.1:8080,strip` flags, or
`etunnel.conf` `[route]` blocks (`--config`; flags > env > config > defaults).

Semantics (identical in every SDK, pinned by tests): longest prefix wins; matching is on segment boundaries (`/api` never matches `/apiv2`); `strip` removes only the path part - the query string is carried over byte-for-byte, an exact hit bottoms out at `/`; no matching route (only possible without a `/` fallback) answers `404` at the SDK edge.

> Redirect note: with `strip`, a backend 302 to an absolute path (`/users/next`) lands on the fallback route on the next hop - backends should return relative redirects or keep the prefix (same as nginx `proxy_pass` with trailing slash; see #155).
