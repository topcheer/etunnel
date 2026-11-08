# etunnel

[![npm](https://img.shields.io/npm/v/@easytunnel/et-sdk?label=npm%20%40easytunnel%2Fet-sdk)](https://www.npmjs.com/package/@easytunnel/et-sdk)
[![crates.io](https://img.shields.io/crates/v/etunnel?label=crates.io%20etunnel)](https://crates.io/crates/etunnel)
[![PyPI](https://img.shields.io/pypi/v/etunnel?label=PyPI%20etunnel)](https://pypi.org/project/etunnel/)
[![NuGet](https://img.shields.io/nuget/v/etunnel?label=NuGet%20etunnel)](https://www.nuget.org/packages/etunnel)
[![Hex](https://img.shields.io/hexpm/v/etunnel?label=Hex%20etunnel)](https://hex.pm/packages/etunnel)
[![LuaRocks](https://img.shields.io/badge/LuaRocks-ggcode.etunnel-8b4bb8?logo=lua)](https://luarocks.org/modules/topcheer/ggcode.etunnel)

A reverse-tunnel service like localhost.run / cloudflare tunnel, but **zero
registration** and **one line of code** to use: an SDK client connects out to
the server, gets a random subdomain with an automatically issued TLS
certificate, and anything on the internet can then reach the developer's local
service through `https://<subdomain>.<domain>`.

```
┌──────────┐  wss (multiplexed HTTP streams)   ┌─────────────┐  https
│ your app  │ ───────────────────────────────▶ │  etunneld   │ ◀──── internet
│ :3000     │ ◀─────────────────────────────── │ + auto-TLS  │       visitors
└──────────┘        one outbound WS            └─────────────┘   *.t.ggcode.uk
```

**Live deployment**: `wss://t.ggcode.uk/tunnel` — tunnels live at
`https://<name>.t.ggcode.uk`.

## Plans

| | Free (anonymous) | Starter | Pro |
|---|---|---|---|
| Registration | none | API key | API key |
| Subdomain | random `word-word-42` | 1 custom | 5 custom |
| Tunnel lifetime | 4 hours | 72 hours | 30 days |
| Bandwidth | 1 Mbps | 10 Mbps | 10 Mbps |

Custom tiers: pass a JSON file to `etunneld -tiers tiers.json` mapping tier id
→ `{name, ttlHours, customDomains, bandwidthMbps}`.

## Quick start

### CLI

```sh
etunnel -server wss://t.ggcode.uk/tunnel 3000
#   https://cosmic-auroch-59.t.ggcode.uk -> 127.0.0.1:3000
#   plan: Free, expires in 4h
```

### Go SDK — one call

```go
import etunnel "etunnel/sdk"

t, err := etunnel.Connect(ctx, etunnel.Options{
    Server:    "wss://t.ggcode.uk/tunnel",
    LocalAddr: "127.0.0.1:3000",
})
fmt.Println(t.URL()) // https://cosmic-auroch-59.t.ggcode.uk
defer t.Close("done")
```

### 20 language SDKs

Single-file, dependency-light SDKs live in [`sdks/`](sdks/README.md)
(Go, Python, JS/Node, TypeScript, Deno, Java, Kotlin, C#, Rust, Swift, Dart,
Ruby, PHP, Perl, Lua, Scala, Elixir, C++, C, Clojure). All honor the same
`ETUNNEL_SERVER` / `ETUNNEL_LOCAL` / `ETUNNEL_TOKEN` / `ETUNNEL_SUBDOMAIN`
environment variables and print `TUNNEL_URL: ...` when run standalone.

### Paid plans (custom subdomains, longer TTL, 10 Mbps)

```sh
curl -X POST https://t.ggcode.uk/api/register \
     -d '{"email":"you@example.com","tier":"starter"}'
# {"apiKey":"et_...","tier":"starter", ...}

etunnel -server wss://t.ggcode.uk/tunnel -token et_... -subdomain mydev 3000
```

## Server (etunneld)

Single static binary, three TLS modes:

```sh
# ACME on-demand per-hostname certificates (HTTP-01, needs :80 reachable)
etunneld -domain t.example.com -addr :443 -http-addr :80 \
         -tls-mode ondemand -acme-dir /var/lib/etunnel/acme -acme-email you@x.com

# Wildcard cert via DNS-01 (Cloudflare/AliDNS/DNSPod credentials via env)
etunneld -domain t.example.com -tls-mode dns -dns-provider cloudflare ...

# Your own cert (hot-reloaded) or plain HTTP behind an LB
etunneld -domain t.example.com -tls-mode static -tls-cert fullchain.pem -tls-key privkey.pem
```

Endpoints:

- `wss://<domain>/tunnel` — SDK WebSocket endpoint (subprotocol `etunnel-v1`)
- `https://<domain>/api/register` — POST `{"email","tier"}` → API key
- `https://<domain>/api/tiers` — plan catalog
- `https://<base>/` — landing page; unknown subdomains get a branded 404
- `127.0.0.1:9090/stats` (admin) — live tunnels, uptime, byte counters
- `127.0.0.1:9090/admin/accounts` (admin, `X-Admin-Token`) — account management

## Features

- HTTP/1.1 reverse proxying by hostname with streaming bodies (Go SDK),
  `X-Forwarded-For`/`X-Forwarded-Proto` injection, chunked transfer, flushes
- WebSocket / 101-upgrade **pass-through** (Go SDK): hijack + raw byte pipe,
  so `wss://<sub>.t.ggcode.uk` works for your local WS server
- Plan-based limits: TTL expiry (server closes with `ttl_expired`), bandwidth
  rate limiting (token bucket, both directions), custom-domain quotas
- Anonymous abuse guard: per-IP concurrent tunnel cap
- Heartbeats (app-level ping/pong + liveness timeout), graceful `bye`,
  clean 502/504 when the local service is down or slow
- Accounts persisted to JSON (atomic writes); tiers configurable via file
- Protocol tolerant to client WS libraries that send binary frames with the
  text opcode (content-sniffed server side)

## Layout

```
protocol/    wire protocol (frames + JSON control messages) shared by all
server/      etunneld: sessions, registry, proxy, tiers, TLS, admin API
sdk/         Go SDK (reference client, full streaming + WS passthrough)
cmd/etunneld server CLI          cmd/etunnel  client CLI
sdks/        20-language clients (see sdks/README.md)
examples/    basic usage example
integration_test.go  end-to-end tests (HTTP, WS, tiers, limits, TTL, -race)
```

## Development

```sh
go build ./... && go test -race ./...   # full suite incl. integration tests
gofmt -w . && go vet ./...
```

Protocol details: [PROTOCOL.md](PROTOCOL.md).
