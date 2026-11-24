# etunnel for Deno

[![JSR](https://img.shields.io/badge/JSR-@ggcode%2Fetunnel-f7df1e?logo=deno)](https://jsr.io/@ggcode/etunnel)

Built-in WebSocket, zero dependencies, published on JSR as `@ggcode/etunnel`.

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

## Install

```sh
deno add jsr:@ggcode/etunnel
# no config needed - import straight from the registry instead:
#   import { connect } from 'jsr:@ggcode/etunnel';
```

Deno >= 1.38 (declared; verified on 2.9.8). `deno add` writes an import-map
entry to `deno.json` (`"@ggcode/etunnel": "jsr:@ggcode/etunnel@^x.y.z"`), so
`import { connect } from '@ggcode/etunnel'` works too. The `jsr:` prefix is
required: `deno add @ggcode/etunnel` fails (looked up on npm). Deno >= 2.9
skips versions published less than 24 h ago by default, so `deno add` may pin
the previous release right after a new one ships.

## AI agent quick-start (copy this prompt)

```text
Integrate etunnel (Deno SDK) into this project to expose the local service on port <PORT> at a public HTTPS URL.

- Package: `@ggcode/etunnel` on JSR · Install: `deno add jsr:@ggcode/etunnel` (or import `jsr:@ggcode/etunnel` directly) · Run with `deno run --allow-net <file>`
- Server: wss://t.etun.uk/tunnel (override with ETUNNEL_SERVER). Anonymous tier = random subdomain, 4h TTL; custom subdomain / longer TTL need a paid token (opts.token or ETUNNEL_TOKEN).
- Minimal embed (verified against the SDK source):
import { connect } from 'jsr:@ggcode/etunnel';

const tunnel = await connect('wss://t.etun.uk/tunnel', <PORT>);
console.log('TUNNEL_URL: ' + tunnel.url);

Deno.addSignalListener('SIGINT', () => {
  tunnel.close();
  Deno.exit(0);
});
- Rules: print the public URL as `TUNNEL_URL: <url>` and keep the process alive; call `tunnel.close()` on shutdown to release the subdomain. This SDK is buffer-based (no WebSocket upgrades, no streaming) - fine for webhooks/APIs/dashboards.
- Do not invent other options. Full API/options: the `API` and `Multi-target routing` sections of this README (`sdks/deno/README.md#api`). Wire protocol: https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md
```

## CLI

```sh
# etunnel.ts = the file from this directory (or the raw URL below, no download step)
deno run --allow-net --allow-env=ETUNNEL_SERVER,ETUNNEL_LOCAL,ETUNNEL_TOKEN,ETUNNEL_SUBDOMAIN,ETUNNEL_RECONNECT etunnel.ts 3000
deno run --allow-net --allow-env=ETUNNEL_SERVER,ETUNNEL_LOCAL,ETUNNEL_TOKEN,ETUNNEL_SUBDOMAIN,ETUNNEL_RECONNECT https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/deno/etunnel.ts 3000
```

The CLI reads `ETUNNEL_*`, so it needs `--allow-env` in addition to
`--allow-net` (`--allow-net` alone stops with `NotCapable: Requires env access
to "ETUNNEL_SERVER"`). No `--allow-read`, `--allow-run` or `--allow-sys` is
needed; the embed below needs `--allow-net` only.

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
import { connect } from 'jsr:@ggcode/etunnel';

const tunnel = await connect('wss://t.etun.uk/tunnel', 3000);
console.log('TUNNEL_URL: ' + tunnel.url);

Deno.addSignalListener('SIGINT', () => {
  tunnel.close();
  Deno.exit(0);
});
```

Run with `deno run --allow-net app.ts`. After `deno add jsr:@ggcode/etunnel` the
import can also be `from '@ggcode/etunnel'`.

## API

| Symbol | Returns / effect |
|---|---|
| `connect(server, local, opts?)` | `Promise<Tunnel>` — resolves after the server's `welcome` frame |
| `Tunnel.url` / `.hostname` / `.subdomain` / `.plan` | facts from the `welcome` frame |
| `Tunnel.resumeToken` | server-signed token; pass it back as `opts.resume` to reclaim the subdomain |
| `Tunnel.close(reason?)` | sends `bye`, closes the socket, frees the subdomain |
| `normalizeRoutes(routes)` / `routeFor(routes, path)` | exported helpers |
| `login(server, opts?)` | `Promise<Credentials>` — browser login; not exercised in this audit |

`opts`:

| Option | Type | Meaning |
|---|---|---|
| `token` | `string` | paid-plan API key: custom subdomain + longer TTL (CLI: `ETUNNEL_TOKEN`) |
| `subdomain` | `string` | request this subdomain instead of a random one (paid plans; CLI: `ETUNNEL_SUBDOMAIN`) |
| `client` | `string` | client identifier sent in `hello` (default `etunnel-deno/<version>`) |
| `resume` | `string` | `resumeToken` of an earlier tunnel: reclaim the same subdomain |
| `routes` | `Route[]` | multi-target routing, see below. `local` stays required: pass any port, it is unused when `routes` is set (`null` throws `etunnel: local address is required`) |
| `onClose` | `(reason: string) => void` | called once when the tunnel ends: your `close()`, a server `bye`, a ping timeout or a dropped socket |
| `wsFactory` | `(url: string) => WebSocket` | WebSocket constructor to use instead of the global `WebSocket` |

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
// local is required even with routes (null throws) - any port, it is unused
const t = await connect('wss://t.example.com/tunnel', 3000, {
  routes: [
    { prefix: '/api', addr: '127.0.0.1:8080', strip: true },
    { prefix: '/',    addr: '127.0.0.1:3000' },
  ],
});
```

Semantics (identical in every SDK, pinned by tests): longest prefix wins; matching is on segment boundaries (`/api` never matches `/apiv2`); `strip` removes only the path part - the query string is carried over byte-for-byte, an exact hit bottoms out at `/`; no matching route (only possible without a `/` fallback) answers `404` at the SDK edge.

> Redirect note: with `strip`, a backend 302 to an absolute path (`/users/next`) lands on the fallback route on the next hop - backends should return relative redirects or keep the prefix (same as nginx `proxy_pass` with trailing slash; see #155).

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `NotCapable: Requires env access to "ETUNNEL_SERVER"` | CLI run with `--allow-net` only | add `--allow-env` (the scoped form is in the CLI section) |
| `npm:@ggcode/etunnel was not found, but a matching jsr package exists` | `deno add @ggcode/etunnel` without the `jsr:` prefix | `deno add jsr:@ggcode/etunnel` |
| `etunnel: local address is required` with `routes` | `connect(server, null, { routes })` - `null` is rejected | pass any port as `local`; it is unused when `routes` is set |
| `deno add` pins the previous version | Deno >= 2.9 skips versions published < 24 h ago | wait, or `deno add jsr:@ggcode/etunnel@<version>` |
