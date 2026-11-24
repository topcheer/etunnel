# etunnel for Rust

[![crates.io](https://img.shields.io/crates/v/etunnel?label=crates.io%20etunnel)](https://crates.io/crates/etunnel)

tungstenite + ureq CLI crate.

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

## Install

```
cargo install etunnel
```

Requires Rust 1.70+. Compiles ~65 crates (about 25 s on an M-series Mac).
There is **no library target**: `cargo add etunnel` + `use etunnel;` does not
resolve - embed by spawning the binary (below).

## AI agent quick-start (copy this prompt)

````text
Integrate etunnel (Rust SDK) into this project to expose the local service on port <PORT> at a public HTTPS URL.

- Package: `etunnel` on crates.io (binary crate only, no library API) · Install: `cargo install etunnel` (~25 s build)
- Server: wss://t.etun.uk/tunnel (override with ETUNNEL_SERVER). Anonymous tier = random subdomain, 4h TTL; custom subdomain / longer TTL need a paid token (opts.token or ETUNNEL_TOKEN).
- Minimal embed (verified against the SDK source - spawn the binary and relay its output, inside fn main() -> Result<(), Box<dyn std::error::Error>>):
```rust
use std::io::{BufRead, BufReader};
use std::process::Command;
let mut child = Command::new("etunnel").arg("3000").env("ETUNNEL_SERVER", "wss://t.etun.uk/tunnel")
    .stdout(std::process::Stdio::piped()).spawn()?;
if let Some(out) = child.stdout.take() {
    for line in BufReader::new(out).lines() { println!("{}", line?); }
}
child.kill().ok();
Ok(())
```
- Rules: the child prints `TUNNEL_URL: <url>` on every (re)connect - relay it and keep the child running; drop it with child.kill() on shutdown (the server frees the subdomain when the socket closes). This SDK is buffer-based (no WebSocket upgrades, no streaming) - fine for webhooks/APIs/dashboards.
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
// the crate ships a binary target only, so drive it as a child process
// (inside `fn main() -> Result<(), Box<dyn std::error::Error>>`):
use std::io::{BufRead, BufReader};
use std::process::Command;

let mut child = Command::new("etunnel")
    .arg("3000")
    .env("ETUNNEL_SERVER", "wss://t.etun.uk/tunnel")
    .stdout(std::process::Stdio::piped())
    .spawn()?;

// the CLI prints `TUNNEL_URL: <url>` on every (re)connect; relay it
if let Some(out) = child.stdout.take() {
    for line in BufReader::new(out).lines() {
        println!("{}", line?);
    }
}
child.kill().ok();   // the socket drops, the server frees the subdomain
Ok(())
```

CLI flags (the real API until a `lib` target exists): `etunnel [local-port-or-addr]`,
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

```sh
etunnel --route /api=127.0.0.1:8080,strip --route /=127.0.0.1:3000
# or in etunnel.conf:  [route] blocks with prefix / addr / strip, run via `etunnel --config etunnel.conf`
```

The equivalent library call (`etunnel::connect_routes`, `etunnel::Route`) exists
in `src/main.rs` and `src/route.rs`, but the crate publishes no `lib` target, so
it is not reachable from a dependent crate yet. CLI precedence: flags > env >
config > defaults.

Semantics (identical in every SDK, pinned by tests): longest prefix wins; matching is on segment boundaries (`/api` never matches `/apiv2`); `strip` removes only the path part - the query string is carried over byte-for-byte, an exact hit bottoms out at `/`; no matching route (only possible without a `/` fallback) answers `404` at the SDK edge.

> Redirect note: with `strip`, a backend 302 to an absolute path (`/users/next`) lands on the fallback route on the next hop - backends should return relative redirects or keep the prefix (same as nginx `proxy_pass` with trailing slash; see #155).
