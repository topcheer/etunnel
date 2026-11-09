# etunnel for Swift

[![Swift](https://img.shields.io/badge/swift-5.9%2B-F05138?logo=swift)](https://www.swift.org)
[![SPM](https://img.shields.io/badge/SPM-compatible-brightgreen)](../../Package.swift)

Foundation-only async client built on `URLSessionWebSocketTask`; a single file, no package dependencies.

Every SDK speaks the same wire protocol against any etunnel server - the default is `wss://t.etun.uk/tunnel`, and `ETUNNEL_SERVER` or the first argument of `connect()` points at your own deployment.

```mermaid
flowchart LR
  V["visitor<br/>https://&lt;subdomain&gt;.<domain>"] -->|HTTPS| S["etunneld<br/>TLS + plan limits"]
  S -->|"wss /tunnel"| C["Swift client<br/>in your process"]
  C -->|HTTP| A["localhost:3000"]
```

## Install

```sh
# single file (Foundation only): copy it into your target
curl -O https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/swift/etunnel.swift

# or build the bundled executable target
swift build -c release
```

## Embed in your app

```swift
import Foundation

let tunnel = Etunnel(server: "wss://t.etun.uk/tunnel", local: "3000")
let url = try await tunnel.connect()
print("public URL: " + url)
```

## CLI

```sh
swift run etunnel 3000
# or: swift build -c release && .build/release/etunnel 3000
```

Prints `TUNNEL_URL: https://<subdomain>.<domain>`; `Ctrl-C` closes the tunnel and the server frees the subdomain immediately.

## API

| Symbol | Returns / effect |
|---|---|
| `Etunnel(server:local:)` | creates the client (no I/O yet) |
| `await connect(token:subdomain:)` | `String` - the public URL, after `welcome` |
| `close()` | graceful shutdown |

Options: Both arguments are optional: `try await tunnel.connect(token: "et_...", subdomain: "myapp")`

The tunnel keeps the URL for the lifetime of the process (or the plan TTL, whichever comes first); the server closes it with `bye` when the TTL expires, and `close()` releases it early.

Each account may hold as many concurrent tunnels as its plan allows (free 2, starter 5, pro 20); anonymous tunnels are additionally capped per source IP. Starting one too many is rejected with `limit_reached`.

## Environment variables

| Variable | Meaning | Default |
|---|---|---|
| `ETUNNEL_SERVER` | Tunnel server WebSocket URL | `wss://t.etun.uk/tunnel` |
| `ETUNNEL_LOCAL` | Local port or `host:port` (the first CLI argument wins) | `3000` |
| `ETUNNEL_TOKEN` | Paid-plan API key (enables custom subdomain + longer TTL) | - |
| `ETUNNEL_SUBDOMAIN` | Requested subdomain, paid plans only | random |

## Notes and limits

- Buffer-based client (no WS upgrades); the [Go SDK](../../sdk) handles those.
- `etunnel.swift` is the entire client and contains no top-level code, so it can be vendored into an app target as is; `main.swift` is only the CLI entry point.

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
