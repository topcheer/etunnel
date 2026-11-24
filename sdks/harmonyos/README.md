# etunnel for HarmonyOS

ArkTS SDK (`Etunnel.ets`, `@ohos.net.websocket`). **Not on ohpm yet** - the
`etunnel` package is unpublished (registry publishing is pending, see
[Publishing status](#publishing-status-ohpm)); copy the source file into your
module instead.

Single-file SDK (`Etunnel.ets`): one outbound WebSocket, zero third-party
dependencies. Port of the [TypeScript SDK](../typescript/README.md) to the
ArkTS strict subset - same wire protocol, same #243 hardening limits, same
reconnect semantics. WebSocket (101) pass-through is not forwarded; use the
[Go SDK](../../sdk/README.md) for those.

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

> **Verification status of this page:** there is no DevEco Studio / hvigor /
> ohpm toolchain on the machine that audited this document, and the repository
> has no HarmonyOS CI job. Every API below was checked statically against
> `Etunnel.ets` (signatures, defaults, log lines); nothing here was executed on
> a device or emulator - see [Verify](#verify).

## AI agent quick-start (copy this prompt)

```text
Integrate etunnel (HarmonyOS ArkTS SDK) into this project to expose the local service on port <PORT> at a public HTTPS URL.

- Package: `etunnel` on ohpm is NOT published - copy the single source file sdks/harmonyos/Etunnel.ets into your module (e.g. entry/src/main/ets/Etunnel.ets) and import it relatively; no third-party dependency.
- Server: wss://t.etun.uk/tunnel. Anonymous tier = random subdomain, 4h TTL; a static subdomain / longer TTL need an API token (opts.token; a free key holds one static subdomain).
- Minimal embed (verified against the SDK source):
  import { connect } from './Etunnel';
  const t = await connect('wss://t.etun.uk/tunnel', '<PORT>');
  console.log('TUNNEL_URL: ' + t.url);
  // ... later: t.close('done');
- Rules: print the public URL as `TUNNEL_URL: <url>` and keep the ability alive while the URL is needed; call t.close(reason) to release the subdomain. Declare ohos.permission.INTERNET in the module.json5 of the consuming app. connect() is one-shot - use startSupervise() for automatic reconnect. This SDK is buffer-based (no WebSocket upgrades, no streaming) - fine for webhooks/APIs/dashboards.
- Do not invent other options. Full API/options: https://github.com/topcheer/etunnel/blob/main/sdks/harmonyos/README.md#api. Wire protocol: https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md
```

## Platform matrix

| | |
|---|---|
| Language / runtime | ArkTS (HarmonyOS stage model) |
| Transport | `@ohos.net.websocket` (subprotocol `etunnel-v1`) |
| Local replay | `@ohos.net.http` (no `fetch` in ArkTS) |
| Package | HAR via `oh-package.json5` + hvigor config in this directory (not published to ohpm) |
| Dependencies | none (System API only) |
| Body model | buffered per stream (32 KiB chunks) |
| WebSocket (101) pass-through | no - use the [Go SDK](../../sdk/README.md) |
| System API requirements | `@ohos.net.websocket` (API 7+), `@ohos.net.http`; a raw `Sec-WebSocket-Protocol` header is sent as well so older stacks still negotiate `etunnel-v1` |

## Install

**Install from source (the only working route until ohpm publishing lands):**

```sh
cp sdks/harmonyos/Etunnel.ets <your-module>/src/main/ets/Etunnel.ets
```

## Embed

Then import it by relative path and open a tunnel:

```ts
import { connect } from './Etunnel';   // adjust to wherever the file lives

// Expose your on-device service (e.g. a local web server) publicly.
// local is a STRING: a port ("3000") or "host:port".
const t = await connect('wss://t.etun.uk/tunnel', '3000');
console.log('TUNNEL_URL: ' + t.url);   // https://<name>.t.etun.uk

// Multi-target routing under one public hostname (PROTOCOL.md section 2):
const routed = await connect('wss://t.etun.uk/tunnel', '3000', {
  routes: [
    { prefix: '/api', addr: '127.0.0.1:8080' },
    { prefix: '/admin', addr: '127.0.0.1:9090', strip: true },
  ],
});

t.close('done');
```

The consuming app needs the INTERNET permission in its own
`module.json5` (`requestPermissions: [{ name: 'ohos.permission.INTERNET' }]`).

### Reconnects

`connect()` is one-shot; `startSupervise()` (exported) is the reconnect loop:
exponential backoff 500ms -> 30s with +-20% jitter, reset on success, printing
`TUNNEL_URL:` again on every session, and replaying the server-signed
`resumeToken` so the same subdomain is reclaimed instead of re-randomised
(matches every other SDK). It returns a handle - `handle.stop()` cancels the
loop (resolves `handle.done`); config-class failures (invalid server URL)
are capped at 3 dial attempts, then the loop gives up instead of retrying
forever (#283-3). For long-lived tunnels run the supervisor from a
long-running task / continuous task so the system does not freeze the
background ability.

### Environment variables

HarmonyOS apps have no process environment, so the cross-SDK `ETUNNEL_*`
contract is exposed as a pure function - feed it a string map (preferences,
remote config, a debug launcher) and get identical behaviour:

```ts
import { configFromEnv, connect, startSupervise } from './Etunnel';

const cfg = configFromEnv({
  'ETUNNEL_SERVER': 'wss://t.etun.uk/tunnel',
  'ETUNNEL_LOCAL': '8080',
  'ETUNNEL_TOKEN': '<paid-plan api key>',
  'ETUNNEL_SUBDOMAIN': '',        // blank = random
  'ETUNNEL_RECONNECT': '1',       // '0' = single-shot, like every other SDK
});
// cfg.reconnect is the mapped ETUNNEL_RECONNECT: supervise unless it was '0'.
const handle = cfg.reconnect
  ? startSupervise(cfg.server, cfg.local, { token: cfg.token, subdomain: cfg.subdomain })
  : undefined;
if (!cfg.reconnect) {
  const t = await connect(cfg.server, cfg.local, { token: cfg.token, subdomain: cfg.subdomain });
  console.log('TUNNEL_URL: ' + t.url);
}
// later: if (handle) handle.stop();
```

`startSupervise()` returns a `SupervisorHandle` synchronously (`{ stop(), done }`),
not a promise - keep the handle so you can stop the loop.

| Variable | Meaning | Default |
|---|---|---|
| `ETUNNEL_SERVER` | tunnel server WebSocket URL | `wss://t.etun.uk/tunnel` |
| `ETUNNEL_LOCAL` | local port or `host:port` | `3000` |
| `ETUNNEL_TOKEN` | API key | - |
| `ETUNNEL_SUBDOMAIN` | requested subdomain (needs a key that holds it) | random |
| `ETUNNEL_RECONNECT` | `0` = single-shot, anything else = reconnect loop | reconnect |

`configFromEnv` only maps the keys (`reconnect` is `false` only for
`ETUNNEL_RECONNECT=0`); the snippet above acts on it, `connect()` itself does
not.

## CLI

None. There is no CLI binary and no `login()` loopback OAuth on this platform
(no local HTTP server / `open` in an app sandbox) - obtain the API key from
the portal at `/me` in a browser and pass it as `ETUNNEL_TOKEN` / `opts.token`.

## API

Signatures as exported from `Etunnel.ets`:

```ts
export interface Route   { prefix: string; addr: string; strip?: boolean }
export interface Options { routes?: Route[]; token?: string; subdomain?: string; client?: string;
                           resume?: string; onClose?: (reason: string) => void; welcomeTimeoutMs?: number }
export interface Tunnel  { url: string; hostname: string; subdomain: string; resumeToken: string;
                           plan: string; close(reason?: string): void }
export function connect(server: string, local: string, opts: Options = {}): Promise<Tunnel>
export function startSupervise(server: string, local: string, opts: Options = {}): SupervisorHandle
export interface SupervisorHandle { stop(): void; done: Promise<void> }
export function configFromEnv(env: Record<string, string>): EtunnelConfig
```

- `connect()` resolves only after the server's `welcome` and throws on a
  handshake error or when no welcome arrives within 15 s
  (`welcomeTimeoutMs` overrides). An empty `local` throws
  `etunnel: local address is required`.
- `Tunnel.resumeToken` lets a later `connect(..., { resume })` reclaim the
  same subdomain; `startSupervise()` does this for you.
- Also exported for tests/embedders: `normalizeRoutes`, `routeFor`,
  `utf8Encode`, `utf8Decode`, `httpMethodFor`.
- `connect()` itself prints nothing; `startSupervise()` prints
  `TUNNEL_URL: <url>` on every successful session and `etunnel: ...` errors.

## Publishing status (ohpm)

**Not published.** `etunnel` does not exist on ohpm (checked 2026-11-24); an
`ohpm install etunnel` fails today. Publishing needs an open-source Huawei
developer account and package review. Until it lands, install from source as
shown above.

If you prefer a local HAR over copying the file, open this directory as a HAR
module in DevEco Studio and reference it from your app; the import is then
`import { connect } from 'etunnel';` (the name in `oh-package.json5`):

```sh
# UNVERIFIED (no ohpm on the auditing machine): local-directory install
ohpm install ./path/to/etunnel-harmonyos
# or reference the directory in oh-package.json5 dependencies
```

This is a documented follow-up, not a blocker for the SDK itself.

## CI toolchain limitation

GitHub Actions has **no official DevEco Studio / hvigor toolchain image**, and
the HarmonyOS command-line build (hvigor + SDK download) requires accepting
Huawei licenses against developer accounts - there is nothing to wire a
`publish-harmonyos` CI job to today. As a result:

- CI builds **do not** package the HAR (no `pack-harmonyos` in the workflow;
  do not add one until an official/licensed toolchain image exists).
- There is no automated check for this SDK in the repository; verification is
  static (see below).
- DevEco Studio users: open this directory as a HAR module and run
  `hvigorw assembleHar` to build the HAR locally. The file is named after the
  version in `oh-package.json5`, which `bump-release.py` stamps from `VERSION`
  (`etunnel-1.7.36.har` at the time of writing; fixed in 1.7.36 - earlier
  releases left it at 1.4.0).

## Verify

```sh
# static, no DevEco needed: strict type check of the SDK's own code.
# tsc rejects the .ets extension and knows nothing about @ohos.*, so copy the
# file and hand it two untyped module stubs:
cp sdks/harmonyos/Etunnel.ets /tmp/Etunnel.ts
printf "declare module '@ohos.net.websocket' { export const webSocket: any; }\ndeclare module '@ohos.net.http' { export const http: any; }\n" > /tmp/ohos-stubs.d.ts
tsc --strict --noEmit --target ES2020 --lib ES2020,DOM /tmp/ohos-stubs.d.ts /tmp/Etunnel.ts
# -> only TS2503 "Cannot find namespace 'webSocket'/'http'" (3 hits), i.e. the
#    platform types the stubs do not model; no errors in the SDK's own logic.

# full DevEco build (requires DevEco Studio / command-line tools + license)
hvigorw assembleHar
```

The `tsc` line above was run (TypeScript 6.0.2) with the result shown;
compiling the two snippets on this page the same way gives 0 non-platform
errors. `hvigorw assembleHar` was not run. ArkTS restrictions honoured in the source: no
`any`/`unknown`, no optional-call syntax, no object spread, explicit
interfaces for all wire messages, `Uint8Array`/`DataView` framing instead of
`Buffer`.

## Limits

- Buffered bodies per stream (32 KiB frames): right for webhooks, JSON APIs,
  dashboards; not for large uploads, SSE, or WebSocket pass-through.
- One WebSocket per tunnel; concurrency is single-threaded on the ArkTS event
  loop, like the Node/TS/Deno SDKs.
- Multi-value request headers are comma-joined into a single value - the OHOS
  `http` header map is single-valued - so values that themselves contain
  commas cannot round-trip (#283-5, documented limitation).
- HTTP methods are passed through verbatim (PROPFIND/REPORT/custom verbs are
  forwarded as-is, never rewritten to GET; #283-2).

## License

MIT - see [LICENSE](../../LICENSE).
