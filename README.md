# etunnel

<img src="assets/logo.svg" alt="etunnel" width="360">


[![npm](https://img.shields.io/npm/v/@easytunnel/et-sdk?label=npm%20%40easytunnel%2Fet-sdk)](https://www.npmjs.com/package/@easytunnel/et-sdk)
[![crates.io](https://img.shields.io/crates/v/etunnel?label=crates.io%20etunnel)](https://crates.io/crates/etunnel)
[![PyPI](https://img.shields.io/pypi/v/etunnel?label=PyPI%20etunnel)](https://pypi.org/project/etunnel/)
[![NuGet](https://img.shields.io/nuget/v/etunnel?label=NuGet%20etunnel)](https://www.nuget.org/packages/etunnel)
[![Hex](https://img.shields.io/hexpm/v/etunnel?label=Hex%20etunnel)](https://hex.pm/packages/etunnel)
[![LuaRocks](https://img.shields.io/badge/LuaRocks-ggcode.etunnel-8b4bb8?logo=lua)](https://luarocks.org/modules/topcheer/ggcode.etunnel)
[![pub.dev](https://img.shields.io/pub/v/etunnel?label=pub.dev%20etunnel)](https://pub.dev/packages/etunnel)
[![Packagist](https://img.shields.io/packagist/v/ggcode/etunnel)](https://packagist.org/packages/ggcode/etunnel)
[![Clojars](https://img.shields.io/badge/Clojars-dev.ggcode%2Fetunnel-clj-5881d8?logo=clojure)](https://clojars.org/dev.ggcode/etunnel-clj)
[![JSR](https://img.shields.io/badge/JSR-@ggcode%2Fetunnel-f7df1e?logo=deno)](https://jsr.io/@ggcode/etunnel)
[![Go](https://img.shields.io/github/go-mod/go-version/topcheer/etunnel?label=Go%20module)](https://pkg.go.dev/github.com/topcheer/etunnel)
[![Maven Central](https://img.shields.io/maven-central/v/dev.ggcode/etunnel?label=Maven%20Central)](https://central.sonatype.com/artifact/dev.ggcode/etunnel)

A reverse-tunnel service like localhost.run / cloudflare tunnel, but **zero
registration** and **one line of code** to use: an SDK client connects out to
the server, gets a random subdomain with an automatically issued TLS
certificate, and anything on the internet can then reach the developer's local
service through `https://<subdomain>.<domain>`.

```
┌──────────┐  wss (multiplexed HTTP streams)   ┌─────────────┐  https
│ your app  │ ───────────────────────────────▶ │  etunneld   │ ◀──── internet
│ :3000     │ ◀─────────────────────────────── │ + auto-TLS  │       visitors
└──────────┘        one outbound WS            └─────────────┘   *.t.etun.uk
```

**Live deployment**: `wss://t.etun.uk/tunnel` — tunnels live at
`https://<name>.t.etun.uk`.

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
etunnel -server wss://t.etun.uk/tunnel 3000
#   https://cosmic-auroch-59.t.etun.uk -> 127.0.0.1:3000
#   plan: Free, expires in 4h
```

### Go SDK — one call

```go
import etunnel "etunnel/sdk"

t, err := etunnel.Connect(ctx, etunnel.Options{
    Server:    "wss://t.etun.uk/tunnel",
    LocalAddr: "127.0.0.1:3000",
})
fmt.Println(t.URL()) // https://cosmic-auroch-59.t.etun.uk
defer t.Close("done")
```

### 20 language SDKs

Single-file, dependency-light SDKs live in [`sdks/`](sdks/README.md)
(Go, Python, JS/Node, TypeScript, Deno, Java, Kotlin, C#, Rust, Swift, Dart,
Ruby, PHP, Perl, Lua, Scala, Elixir, C++, C, Clojure). All honor the same
`ETUNNEL_SERVER` / `ETUNNEL_LOCAL` / `ETUNNEL_TOKEN` / `ETUNNEL_SUBDOMAIN`
environment variables and print `TUNNEL_URL: ...` when run standalone.

### Paid plans (custom subdomains, longer TTL, 10 Mbps)

Sign in and subscribe from the portal — checkout activates the API key instantly:

```sh
open https://t.etun.uk/pricing   # sign in (Google/GitLab/GitHub/Apple), pick a plan
# you receive: an API key + /me where you manage domains, key and subscription

etunnel -server wss://t.etun.uk/tunnel -token et_... -subdomain mydev 3000
```

Signing in claims the account bound to your verified email (an account may be
created by checkout before you ever log in). If that account was created
otherwise, its previous API key is rotated on claim: any tunnel still running
on the old key disconnects and must be restarted with the key from `/me`.

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
- `https://<domain>/api/register` — POST `{"email"}` → free-tier API key (paid tiers require checkout)
- `https://<domain>/api/tiers` — plan catalog
- `https://<base>/` — landing page; unknown subdomains get a branded 404
- `127.0.0.1:9090/stats` (admin) — live tunnels, uptime, byte counters
- `127.0.0.1:9090/admin/accounts` (admin, `X-Admin-Token`) — account management

## Features

- HTTP/1.1 reverse proxying by hostname with streaming bodies (Go SDK),
  `X-Forwarded-For`/`X-Forwarded-Proto` injection, chunked transfer, flushes
- WebSocket / 101-upgrade **pass-through** (Go SDK): hijack + raw byte pipe,
  so `wss://<sub>.t.etun.uk` works for your local WS server
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

## User accounts & login

OAuth login (Google / GitLab / Apple) gates subscriptions: see /login and
/me (reset API key, cancel, custom domains, close account). Custom
subdomains are ownership-gated - paid users claim names they added in /me.
Full credential list: [CREDENTIALS.md](CREDENTIALS.md).

## Running your own server

```sh
etunneld --domain t.example.com --addr :443 --http-addr :80 \
  --tls-mode dns --dns-provider cloudflare --acme-dir /var/lib/etunnel/acme
```

`--alias-domain t.old.com` serves a legacy domain alongside `--domain`
during migrations (static wildcard cert from the acme dir, not renewed).

## Billing (PayPal + Stripe)

Self-hosted payments for paid tiers, zero third-party Go dependencies:

- **User site**: `https://<domain>/pricing` — pick a plan, pay by card
  (Stripe Checkout) or PayPal; the API key is shown on success
- **Providers are enabled purely by environment variables** (empty = off):

```sh
STRIPE_SECRET_KEY=sk_live_...        # from https://dashboard.stripe.com/apikeys
STRIPE_WEBHOOK_SECRET=whsec_...      # webhook endpoint: https://<domain>/webhooks/stripe
PAYPAL_CLIENT_ID=...                 # from https://developer.paypal.com (app credentials)
PAYPAL_SECRET=...
PAYPAL_ENV=sandbox                   # or live
PAYPAL_WEBHOOK_ID=...                # webhook URL: https://<domain>/webhooks/paypal
TIER_STARTER_PRICE_CENTS=300         # optional overrides (default starter=$3, pro=$9)
TIER_PRO_PRICE_CENTS=900
```

- Activation: first payment mints the account + API key for the email;
  renewals/upgrades keep the same key and change the tier
- Admin: `/stats` reports `billing: {stripe, paypal}` enablement
- Webhook signature verification: Stripe HMAC-SHA256 (tolerance 5m),
  PayPal via the verify-webhook-signature API

## Telegram admin bot (optional)

Manage the server from a whitelisted Telegram chat:

```sh
TELEGRAM_BOT_TOKEN=123456:ABC...   # from @BotFather
TELEGRAM_ADMIN_CHAT_ID=123456789   # your chat id (get via @userinfobot)
```

Commands (only the whitelisted chat is answered; everyone else is ignored):

```
/status                     uptime, tunnel/account counts, traffic, billing
/tunnels                    live tunnels: subdomain, plan, owner, ip, streams
/accounts                   registered accounts and tiers
/kick <subdomain>           drop a tunnel
/tier <email> <tier>        change an account's tier
/whois <subdomain>          tunnel owner
```

Payments also push a notification ("💰 email purchased tier") on every
successful activation. Zero new dependencies; long-polling, no public
webhook needed. Either env empty -> bot fully disabled.
