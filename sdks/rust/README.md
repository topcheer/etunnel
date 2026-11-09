# etunnel for Rust

[![crates.io](https://img.shields.io/crates/v/etunnel?label=crates.io%20etunnel)](https://crates.io/crates/etunnel)
[![Rust](https://img.shields.io/badge/rust-edition%202021-dea584)](https://crates.io/crates/etunnel)

Small binary crate (tungstenite + ureq) you install and run beside your service.

Every SDK speaks the same wire protocol against any etunnel server - the default is `wss://t.etun.uk/tunnel`, and `ETUNNEL_SERVER` or the first argument of `connect()` points at your own deployment.

```mermaid
flowchart LR
  V["visitor<br/>https://&lt;subdomain&gt;.<domain>"] -->|HTTPS| S["etunneld<br/>TLS + plan limits"]
  S -->|"wss /tunnel"| C["Rust client<br/>in your process"]
  C -->|HTTP| A["localhost:3000"]
```

## Install

```sh
cargo install etunnel
```

## Embed in your app

```sh
# the crate ships a binary target, so drive it as a child process:
use std::process::Command;

let mut child = Command::new("etunnel")
    .arg("3000")
    .env("ETUNNEL_SERVER", "wss://t.etun.uk/tunnel")
    .spawn()?;

// ... run your service ...

child.kill().ok();
```

## CLI

```sh
etunnel 3000
ETUNNEL_SERVER=wss://t.etun.uk/tunnel etunnel 3000
```

Prints `TUNNEL_URL: https://<subdomain>.<domain>`; `Ctrl-C` closes the tunnel and the server frees the subdomain immediately.

## API

| Symbol | Returns / effect |
|---|---|
| `etunnel [local]` | prints `TUNNEL_URL: https://...` and serves until killed |
| env | `ETUNNEL_SERVER`, `ETUNNEL_TOKEN`, `ETUNNEL_SUBDOMAIN` |

Options: Positional local port or `host:port`; everything else comes from the environment.

The tunnel keeps the URL for the lifetime of the process (or the plan TTL, whichever comes first); the server closes it with `bye` when the TTL expires, and `close()` releases it early.

## Environment variables

| Variable | Meaning | Default |
|---|---|---|
| `ETUNNEL_SERVER` | Tunnel server WebSocket URL | `wss://t.etun.uk/tunnel` |
| `ETUNNEL_LOCAL` | Local port or `host:port` (the first CLI argument wins) | `3000` |
| `ETUNNEL_TOKEN` | Paid-plan API key (enables custom subdomain + longer TTL) | - |
| `ETUNNEL_SUBDOMAIN` | Requested subdomain, paid plans only | random |

## Notes and limits

- Buffer-based client (no WS upgrades); the [Go SDK](../../sdk) handles those.
- No `lib` target yet - `cargo add etunnel` gives you the binary's code, not a library API. Track the issue if you need an embeddable crate.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `ws: cannot connect to ...` | DNS, corporate proxy or firewall blocks the outbound socket | `curl -sI https://t.etun.uk/` from the same host; set `HTTPS_PROXY` if you need one |
| Public URL answers `404 - no tunnel here` | The tunnel process exited, or the plan TTL expired | Restart the client; the subdomain is released on disconnect |
| `error: invalid_token` | `token` set but not a paid-plan API key | Copy the key from `/me` on the portal, or drop the token for an anonymous tunnel |
| `error: too_many_tunnels` | Per-IP anonymous tunnel cap on the server | Close old clients (`lsof -ti:PORT`), or use a paid plan with a token |
| `error: subdomain_taken` | Custom subdomain already claimed by another account | Pick another name, or remove it from the old account in `/me` |
| Request hangs, then `502` | Local service is not listening on the requested port | `curl -sv http://127.0.0.1:3000/` first |
| WebSocket endpoints fail through the tunnel | Buffer-based SDK (all but Go) cannot forward 101 upgrades | Use the [Go SDK](../../sdk) for WebSocket pass-through |

## See also

- [All 20 SDKs](../README.md) - install matrix and capability comparison
- [Wire protocol](../../PROTOCOL.md) - frames, control messages, stream lifecycle
- [Repository](https://github.com/topcheer/etunnel) - server, Go SDK, examples, self-hosting
- [LICENSE](../../LICENSE) - MIT
