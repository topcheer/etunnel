# etunnel (Rust)

Expose a local service to the internet through an [etunnel](https://github.com/topcheer/etunnel)
reverse-tunnel server — one command, zero registration, automatic TLS.

```text
your app :3000 ──wss──▶ etunneld ──https──▶ https://<subdomain>.t.ggcode.uk
```

## Install

```sh
cargo install etunnel
```

Or add it as a dependency:

```sh
cargo add etunnel
```

## CLI usage

```sh
etunnel 3000                      # expose localhost:3000
TUNNEL_URL: https://quiet-frog-42.t.ggcode.uk
```

That URL now routes to your local service from anywhere on the internet.
`Ctrl-C` closes the tunnel.

Environment variables (all optional):

| Variable            | Meaning                                   | Default                      |
|---------------------|-------------------------------------------|------------------------------|
| `ETUNNEL_SERVER`    | tunnel server WebSocket URL               | `wss://t.ggcode.uk/tunnel`   |
| `ETUNNEL_LOCAL`     | local port or `host:port`                 | `3000` (or 1st CLI arg)      |
| `ETUNNEL_TOKEN`     | paid-plan API key                         | -                            |
| `ETUNNEL_SUBDOMAIN` | custom subdomain (paid plans)             | random                       |

## Embedding

The crate is primarily a CLI (see [`src/main.rs`](https://github.com/topcheer/etunnel/blob/main/sdks/rust/src/main.rs)
for the full protocol client); for programmatic use from Rust, the canonical
streaming SDK is the Go package at [`sdk/`](https://github.com/topcheer/etunnel/tree/main/sdk).

## How it works

The client dials one outbound WebSocket to the server, multiplexes HTTP
request/response streams over it, and forwards each request to your local
service. The wire protocol is tiny (JSON control messages + 9-byte-header
binary frames) — see [PROTOCOL.md](https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md).
This port buffers request/response bodies per stream: fine for webhooks,
dashboards and demos.

Server-side it's a single Go binary (`etunneld`) — see the
[main README](https://github.com/topcheer/etunnel) for a 60-second setup and
the public instance at `wss://t.ggcode.uk/tunnel`.

## License

MIT — see [LICENSE](https://github.com/topcheer/etunnel/blob/main/LICENSE).
