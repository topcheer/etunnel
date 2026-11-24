# etunnel for Node.js

[![npm](https://img.shields.io/npm/v/@easytunnel/et-sdk?label=npm%20%40easytunnel%2Fet-sdk)](https://www.npmjs.com/package/@easytunnel/et-sdk)

Expose a local service through an etunnel reverse-tunnel server. Zero dependencies (Node >= 22 built-in WebSocket).

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

## Install

```
npm install @easytunnel/et-sdk
```

Requires Node >= 22 (verified with `@easytunnel/et-sdk` 1.7.36 from npm).

## AI agent quick-start (copy this prompt)

```text
Integrate etunnel (Node.js SDK) into this project to expose the local service on port <PORT> at a public HTTPS URL.

- Package: `@easytunnel/et-sdk` on npm · Install: `npm install @easytunnel/et-sdk`
- Server: wss://t.etun.uk/tunnel (override with ETUNNEL_SERVER). Anonymous tier = random subdomain, 4h TTL; custom subdomain / longer TTL need a paid token (opts.token or ETUNNEL_TOKEN).
- Minimal embed (verified against the SDK source):
import { connect } from '@easytunnel/et-sdk';   // ESM: .mjs, or "type": "module"

const tunnel = await connect('wss://t.etun.uk/tunnel', <PORT>);
console.log('TUNNEL_URL: ' + tunnel.url);

process.on('SIGINT', () => { tunnel.close(); process.exit(0); });
- Rules: print the public URL as `TUNNEL_URL: <url>` and keep the process alive; call `tunnel.close()` on shutdown to release the subdomain. This SDK is buffer-based (no WebSocket upgrades, no streaming) - fine for webhooks/APIs/dashboards.
- Do not invent other options. Full API/options: the `API` and `Multi-target routing` sections of this README (`sdks/node/README.md#api`). Wire protocol: https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md
```

## CLI

```
npx @easytunnel/et-sdk 3000
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

CommonJS (`.cjs`, or `.js` in a package without `"type": "module"`) — no
top-level `await`, so wrap the call:

```js
const { connect } = require('@easytunnel/et-sdk');

(async () => {
  const tunnel = await connect('wss://t.etun.uk/tunnel', 3000);
  console.log('TUNNEL_URL: ' + tunnel.url);

  // keep the process alive; close() sends a clean bye to the server
  process.on('SIGINT', () => { tunnel.close(); process.exit(0); });
})();
```

ESM (`.mjs`, or `.js` with `"type": "module"`):

```js
import { connect } from '@easytunnel/et-sdk';

const tunnel = await connect('wss://t.etun.uk/tunnel', 3000);
console.log('TUNNEL_URL: ' + tunnel.url);

// keep the process alive; close() sends a clean bye to the server
process.on('SIGINT', () => { tunnel.close(); process.exit(0); });
```

The open WebSocket keeps the event loop alive, so the process does not exit
while the tunnel is up.

## API

| Symbol | Returns / effect |
|---|---|
| `connect(server, local, opts?)` | `Promise<Tunnel>` — resolves after the server's `welcome` frame; rejects with `etunnel: local address is required` when `local` is missing and no `routes` are given |
| `Tunnel.url` / `.hostname` / `.subdomain` | public HTTPS URL and its parts, from the `welcome` frame |
| `Tunnel.resumeToken` | server-signed token; pass it back as `opts.resume` to reclaim the subdomain |
| `Tunnel.plan` | plan name from `welcome` (e.g. `Free`) |
| `Tunnel.close(reason?)` | sends `bye`, closes the socket, frees the subdomain |
| `normalizeRoutes(routes)` / `routeFor(routes, path)` | exported helpers: validate and compile a route table / resolve a request path against it |
| `login(server, opts?)` | `Promise<{ api_token, subdomains, plan, expires_at }>` — browser login (loopback callback); not exercised in this audit |

`opts`:

| Option | Type | Meaning |
|---|---|---|
| `token` | `string` | paid-plan API key: custom subdomain + longer TTL (CLI: `ETUNNEL_TOKEN`) |
| `subdomain` | `string` | request this subdomain instead of a random one (paid plans; CLI: `ETUNNEL_SUBDOMAIN`) |
| `client` | `string` | client identifier sent in `hello` (default `etunnel-node/<version>`) |
| `resume` | `string` | `resumeToken` of an earlier tunnel: reclaim the same subdomain |
| `routes` | `Route[]` | multi-target routing, see below; pass `null` as `local` |
| `policy` | `object` | edge protection sent as `hello.policy`: `basicAuth`, `allowIPs`, `webhooks` ([PROTOCOL.md §1](../../PROTOCOL.md)) |
| `onClose` | `(reason: string) => void` | called once when the tunnel ends: your `close()`, a server `bye`, a ping timeout or a dropped socket |
| `wsFactory` | `(url: string) => WebSocket` | WebSocket constructor to use instead of the global `WebSocket` (the CLI injects one that captures its socket); the global is never patched |

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

```js
const t = await connect('wss://t.example.com/tunnel', null, {
  routes: [
    { prefix: '/api', addr: '127.0.0.1:8080', strip: true },
    { prefix: '/',    addr: '127.0.0.1:3000' },
  ],
});
```

Semantics (identical in every SDK, pinned by tests): longest prefix wins; matching is on segment boundaries (`/api` never matches `/apiv2`); `strip` removes only the path part - the query string is carried over byte-for-byte, an exact hit bottoms out at `/`; no matching route (only possible without a `/` fallback) answers `404` at the SDK edge.

> Redirect note: with `strip`, a backend 302 to an absolute path (`/users/next`) lands on the fallback route on the next hop - backends should return relative redirects or keep the prefix (same as nginx `proxy_pass` with trailing slash; see #155).

## Known issues

- **Fixed in 1.7.36.** npm 1.7.0-1.7.35 were published without `route.js`, so
  `require('@easytunnel/et-sdk')` threw `Cannot find module './route'` and
  `npx @easytunnel/et-sdk 3000` exited on start. 1.7.36 ships the file (verified
  from the registry with an empty npm cache), and the publish job now
  smoke-tests the tarball before uploading.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `Cannot find module './route'` on `require` / `npx` | npm 1.7.0-1.7.35 (published without `route.js`) | upgrade to >= 1.7.36; retest with an empty npx cache |
| `etunnel: local address is required` | `connect(server)` without `local` or `routes` | pass a port, or `null` together with `opts.routes` |
| `404 page not found (no route matches this path)` | `routes` table has no `/` fallback | add `{ prefix: '/', addr: '127.0.0.1:3000' }` |
| `etunnel: ws: cannot connect to <server>` | wrong `ETUNNEL_SERVER` / server not reachable (a bad Host answers `404 - no tunnel here`) | check the WebSocket URL, e.g. `curl -sI https://t.etun.uk/` |
