# etunnel for TypeScript

[![npm](https://img.shields.io/npm/v/@easytunnel/et-sdk?label=npm%20%40easytunnel%2Fet-sdk)](https://www.npmjs.com/package/@easytunnel/et-sdk) (the JS package used from TypeScript)

Same client as the Node SDK, typed and dependency-free: `etunnel.ts` is a single
file you vendor, or you call the npm package `@easytunnel/et-sdk` from
TypeScript (plain JS, no bundled types). There is no `etunnel-ts` package on npm.

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

## Install

```sh
# option A - vendor the single typed file (raw URL, -L follows GitHub's redirect)
curl -fsSL -o etunnel.ts https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/typescript/etunnel.ts

# option B - the npm package, used from TypeScript (plain JS, no bundled types)
npm install @easytunnel/et-sdk
```

Node >= 22 runs `.ts` directly (type stripping; verified on 22.23.5 and 26.13.1)
or compile with `tsc`. Verified with `etunnel.ts` and `@easytunnel/et-sdk` 1.7.36.

## AI agent quick-start (copy this prompt)

```text
Integrate etunnel (TypeScript SDK) into this project to expose the local service on port <PORT> at a public HTTPS URL.

- Package: `@easytunnel/et-sdk` on npm (plain JS, no bundled types) - there is no `etunnel-ts` package on npm; or vendor the typed `etunnel.ts` · Install: `npm install @easytunnel/et-sdk`
- Server: wss://t.etun.uk/tunnel (override with ETUNNEL_SERVER). Anonymous tier = random subdomain, 4h TTL; custom subdomain / longer TTL need a paid token (opts.token or ETUNNEL_TOKEN).
- Minimal embed (verified against the SDK source):
// ESM only: a .mts file, or "type": "module" in package.json
import { connect } from '@easytunnel/et-sdk';

const tunnel = await connect('wss://t.etun.uk/tunnel', <PORT>);
console.log('TUNNEL_URL: ' + tunnel.url);

process.on('SIGINT', () => { tunnel.close(); process.exit(0); });
- Rules: print the public URL as `TUNNEL_URL: <url>` and keep the process alive; call `tunnel.close()` on shutdown to release the subdomain. This SDK is buffer-based (no WebSocket upgrades, no streaming) - fine for webhooks/APIs/dashboards.
- Do not invent other options. Full API/options: the `API` and `Multi-target routing` sections of this README (`sdks/typescript/README.md#api`). Wire protocol: https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md
```

## CLI

```
node etunnel.ts 3000
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

With the vendored file (install option A) - typed, no build step. Importing it is
side-effect free since 1.7.36 (earlier versions also started the CLI):

```ts
import { connect } from './etunnel.ts';

const tunnel = await connect('wss://t.etun.uk/tunnel', 3000);
console.log('TUNNEL_URL: ' + tunnel.url);
console.log('plan:', tunnel.plan);

process.on('SIGINT', () => { tunnel.close(); process.exit(0); });
```

Run it with `node app.mts` (type stripping; verified on Node 22.23.5 and
26.13.1). ESM only: a `.mts` file, or `.ts` in a package that is not
`"type": "commonjs"`. Do not run this file through a strict `tsc` build - see
Known issues.

Or through the npm package (option B), from an ESM file (`.mts`, or `.ts` with
`"type": "module"` in package.json):

```ts
import { connect } from '@easytunnel/et-sdk';

const tunnel = await connect('wss://t.etun.uk/tunnel', 3000);
console.log('TUNNEL_URL: ' + tunnel.url);
console.log('plan:', tunnel.plan);

process.on('SIGINT', () => { tunnel.close(); process.exit(0); });
```

The npm package ships no `.d.ts`, so `tsc --strict` reports `TS7016` on the
import. Add a declaration file (type-checked with `strict`):

```ts
// et-sdk.d.ts
declare module '@easytunnel/et-sdk' {
  export interface Route { prefix: string; addr: string; strip?: boolean }
  export interface Options {
    routes?: Route[];
    token?: string;
    subdomain?: string;
    client?: string;
    resume?: string;
    onClose?: (reason: string) => void;
    wsFactory?: (url: string) => WebSocket;
  }
  export interface Tunnel {
    url: string;
    hostname: string;
    subdomain: string;
    resumeToken: string;
    plan: string;
    close(reason?: string): void;
  }
  export function connect(server: string, local: string | number | null, opts?: Options): Promise<Tunnel>;
  export function normalizeRoutes(routes: Route[]): Route[];
  export function routeFor(routes: Route[], pathAndQuery: string): { addr: string; path: string } | null;
  export function login(server: string, opts?: { openBrowser?: (url: string) => void; timeoutMs?: number }): Promise<{ api_token: string; subdomains: string[] | null; plan: string; expires_at: number }>;
}
```


## API

| Symbol | Returns / effect |
|---|---|
| `connect(server, local, opts?)` | `Promise<Tunnel>` — resolves after the server's `welcome` frame |
| `Tunnel.url` / `.hostname` / `.subdomain` / `.plan` | connection facts from the `welcome` frame |
| `Tunnel.resumeToken` | server-signed token; pass it back as `opts.resume` to reclaim the subdomain |
| `Tunnel.close(reason?)` | sends `bye`, closes the socket, frees the subdomain |
| `normalizeRoutes(routes)` / `routeFor(routes, path)` | exported helpers (same semantics as the Node SDK) |
| `login(server, opts?)` | `Promise<Credentials>` — browser login; not exercised in this audit |

`opts`:

| Option | Type | Meaning |
|---|---|---|
| `token` | `string` | paid-plan API key: custom subdomain + longer TTL (CLI: `ETUNNEL_TOKEN`) |
| `subdomain` | `string` | request this subdomain instead of a random one (paid plans; CLI: `ETUNNEL_SUBDOMAIN`) |
| `client` | `string` | client identifier sent in `hello` (default `etunnel-ts/<version>`) |
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

## Known issues

- **`etunnel-ts` is not on npm.** `https://registry.npmjs.org/etunnel-ts` answers
  404 (checked 2026-11-24). `sdks/typescript/package.json` keeps the name, but
  nothing is published under it — do not `npm install etunnel-ts`.
- **Fixed in 1.7.37.** `tsc --strict` rejected `etunnel.ts` <= 1.7.36 itself (TypeScript 5.9,
  `@types/node` 22, `module`/`moduleResolution` `NodeNext`): six errors -
  `frame()` is declared with a `Buffer` payload but is called with a
  `Uint8Array` / without a payload (lines 284, 285, 301, 325), a
  `Buffer<ArrayBufferLike>` body mismatch (line 319) and, when the file would
  compile to CommonJS, the `import.meta` main guard (line 417). Runtime under
  Node's type stripping is unaffected; the npm-package path with the declaration
  file below type-checked clean). 1.7.37 type-checks clean on every path and
  `make verify` runs the strict check.
- **Fixed in 1.7.36.** Importing the vendored `etunnel.ts` (<= 1.7.35) also
  started the CLI supervisor - a second tunnel plus SIGINT/SIGTERM handlers on
  the importing process; and `@easytunnel/et-sdk` 1.7.0-1.7.35 were published
  without `route.js`. Both verified fixed against 1.7.36.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `etunnel: local address is required` with `routes` | `connect(server, null, { routes })` - `null` is rejected | pass any port as `local`; it is unused when `routes` is set |
| Two `TUNNEL_URL:` lines after `import { connect } from './etunnel.ts'` | `etunnel.ts` <= 1.7.35 also started its CLI (fixed in 1.7.36) | upgrade to 1.7.36 |
| `TS7016` / `implicitly has an 'any' type` | the npm package ships no `.d.ts` | add the declaration file from the Embed section |
| `Cannot use import statement outside a module` / `TS1309` | the `.ts` file lives in a CommonJS project | use `.mts`, or `"type": "module"` in package.json |
| `Cannot find module './route'` on import / `npx` | npm 1.7.0-1.7.35 (published without `route.js`) | upgrade to >= 1.7.36 |
