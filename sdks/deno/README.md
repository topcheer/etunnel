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
| `ETUNNEL_SUBDOMAIN` | custom subdomain (paid plans) | random |

## License

MIT — see [LICENSE](../../LICENSE). Buffer-based protocol client: request and
response bodies are buffered per stream (fine for webhooks/dashboards/demos;
use the [Go SDK](../../sdk) for streaming and WebSocket pass-through).
