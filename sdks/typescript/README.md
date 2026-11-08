# etunnel for TypeScript

[![npm](https://img.shields.io/npm/v/etunnel-ts?label=npm%20etunnel-ts)](https://www.npmjs.com/package/etunnel-ts)

Same implementation as the Node SDK, with types.

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.ggcode.uk/tunnel`).

## Install

```
npm install etunnel-ts
```

Node >= 22 (type stripping) or tsc.

## CLI

```
node node_modules/etunnel-ts/etunnel.ts 3000
```

Prints `TUNNEL_URL: https://<subdomain>.t.ggcode.uk` — that URL now routes to
your local service. Ctrl-C closes the tunnel.

## Embed

```ts
import { connect } from 'etunnel-ts';
const t = await connect('wss://t.ggcode.uk/tunnel', 3000);
console.log(t.url);
```

## Environment variables

| Variable | Meaning | Default |
|---|---|---|
| `ETUNNEL_SERVER` | tunnel server WebSocket URL | `wss://t.ggcode.uk/tunnel` |
| `ETUNNEL_LOCAL` | local port or `host:port` (or 1st CLI arg) | `3000` |
| `ETUNNEL_TOKEN` | paid-plan API key (optional) | - |
| `ETUNNEL_SUBDOMAIN` | custom subdomain (paid plans) | random |

## License

MIT — see [LICENSE](../../LICENSE). Buffer-based protocol client: request and
response bodies are buffered per stream (fine for webhooks/dashboards/demos;
use the [Go SDK](../../sdk) for streaming and WebSocket pass-through).
