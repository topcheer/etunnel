# etunnel for Deno

[![JSR](https://img.shields.io/badge/JSR-@ggcode%2Fetunnel-f7df1e?logo=deno)](https://jsr.io/@ggcode/etunnel)

Built-in WebSocket, zero dependencies.

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

## Install

```
deno add @ggcode/etunnel
```

Deno >= 1.38.

## CLI

```
deno run --allow-net etunnel.ts 3000
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

```ts
import { connect } from '@ggcode/etunnel';
const t = await connect('wss://t.etun.uk/tunnel', 3000);
```

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

```ts
const t = await connect('wss://t.example.com/tunnel', null, {
  routes: [
    { prefix: '/api', addr: '127.0.0.1:8080', strip: true },
    { prefix: '/',    addr: '127.0.0.1:3000' },
  ],
});
```

Semantics (identical in every SDK, pinned by tests): longest prefix wins; matching is on segment boundaries (`/api` never matches `/apiv2`); `strip` removes only the path part - the query string is carried over byte-for-byte, an exact hit bottoms out at `/`; no matching route (only possible without a `/` fallback) answers `404` at the SDK edge.
