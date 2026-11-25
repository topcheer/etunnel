# etunnel for HarmonyOS

ArkTS SDK (`Etunnel.ets`, `@ohos.net.websocket`). **Not on ohpm yet** - the
`etunnel` package is unpublished; the publishing pipeline is in place
(`make pack-harmonyos` builds the HAR, CI `publish-ohpm` pushes it) and the
first release is pending, see [Publishing status](#publishing-status-ohpm).
Until then copy the source file into your module instead.

Single-file SDK (`Etunnel.ets`): one outbound WebSocket, zero third-party
dependencies. Port of the [TypeScript SDK](../typescript/README.md) to the
ArkTS strict subset - same wire protocol, same #243 hardening limits, same
reconnect semantics. WebSocket (101) pass-through is not forwarded; use the
[Go SDK](../../sdk/README.md) for those.

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

> **Verification status of this page:** there is no DevEco Studio / hvigor /
> ohpm toolchain on the machine that audited this document. The HAR build
> (`make pack-harmonyos`) is exercised - it type-checks the SDK strictly with
> `tsc` and emits the HAR - and CI has a `publish-ohpm` job; the wire
> behaviour (`hello.version`, `client_outdated`) is exercised by
> `tests/run.sh` under Node, which needs no DevEco toolchain. Nothing here
> was executed on a device or emulator - see [Verify](#verify).

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
| Package | HAR built by `make pack-harmonyos` (tsc-compiled from `Etunnel.ets`, same layout as a hvigor HAR); pushed to ohpm by CI `publish-ohpm` - not on ohpm until the first release |
| Dependencies | none (System API only) |
| Body model | buffered per stream (32 KiB chunks) |
| WebSocket (101) pass-through | no - use the [Go SDK](../../sdk/README.md) |
| System API requirements | `@ohos.net.websocket` (API 7+), `@ohos.net.http`; a raw `Sec-WebSocket-Protocol` header is sent as well so older stacks still negotiate `etunnel-v1` |

## Install

**Until the first ohpm release lands, install from source:**

```sh
cp sdks/harmonyos/Etunnel.ets <your-module>/src/main/ets/Etunnel.ets
```

**Once published** (the package name is `etunnel`, see below):

```sh
ohpm install etunnel
```

```ts
import { connect } from 'etunnel';   // HAR entry: index.js re-exports Etunnel.js
```

The HAR can also be installed from a local file without the registry:
`ohpm install ./etunnel-harmonyos-<version>.har` (`make pack-harmonyos` writes
it to `dist/packages/`).

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
export const VERSION = '1.7.39'                         // stamped from VERSION by scripts/bump-release.py
export class ServerError extends Error { code: string } // an `error` frame from the server
```

- `hello` reports the SDK version: `version` is `VERSION` and `client` defaults
  to `etunnel-harmonyos/<VERSION>` (unless `opts.client` is set). Captured on
  the wire: `{"type":"hello","proto":1,"version":"1.7.39","client":"etunnel-harmonyos/1.7.39"}`.
- When the server answers the handshake with an `error` frame, `connect()`
  rejects with a `ServerError`: `code` is the machine-readable code and
  `message` is the server's text verbatim. For `client_outdated` that text
  names the required SDK floor and the upgrade URL
  (`https://github.com/topcheer/etunnel`); `startSupervise()` treats
  `client_outdated` and `proto_version` as permanent - it prints the message
  and stops instead of re-dialing. An `error` frame after the handshake closes
  the tunnel and reports `server error: <code>: <message>` through `onClose`.

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

**Not published yet - pipeline ready.** `etunnel` does not exist on ohpm
(`GET https://ohpm.openharmony.cn/ohpm/etunnel` answers 404, re-checked
2026-11-25); `ohpm install etunnel` fails until the first release.

How a release reaches ohpm:

1. `make pack-harmonyos` -> `dist/packages/etunnel-harmonyos-<version>.har`
   (`scripts/pack-harmonyos.sh`: compiles `Etunnel.ets` with `tsc` into the
   layout hvigor emits for an ArkTS-only HAR - `package/oh-package.json5`
   with `main: index.js` / `types: index.d.ts`, `index.js`, `index.d.ts`,
   `src/main/ets/Etunnel.{js,d.ts}`, `src/main/module.json`). The version is
   read from `oh-package.json5`, which `bump-release.py` stamps from `VERSION`.
2. `scripts/publish-ohpm.sh <har>` pushes it over the registry's npm-protocol
   API (`PUT https://ohpm.openharmony.cn/ohpm/etunnel` with the tgz as a base64
   `_attachments` entry, `Authorization: Bearer $OHPM_TOKEN`). The probe is
   idempotent (#412): only an explicit 404 counts as "not published". CI job
   `publish-ohpm` runs both steps on every `v*` tag with the `OHPM_TOKEN`
   secret.

The first publish is a maintainer step (`OHPM_TOKEN=... scripts/publish-ohpm.sh
dist/packages/etunnel-harmonyos-<version>.har`, or the tag's CI run once the
secret is configured). After it lands, install with `ohpm install etunnel` and
`import { connect } from 'etunnel'`. Until then, install from source as shown
above.

## CI packaging (no DevEco Studio)

`hvigorw assembleHar` needs a hvigor *project* shell plus the OHOS SDK
(ets-loader), which only ships with DevEco Studio / the login-gated
"Command Line Tools" download - so CI cannot run it. Instead:

- `scripts/pack-harmonyos.sh` compiles the SDK with `tsc` (`--strict`, the
  same ArkTS subset the file is written in; the two `@ohos.*` imports resolve
  against `types/ohos-stub.d.ts` at build time only) and lays the result out
  like a published HAR. `make pack-all` includes it.
- `scripts/publish-ohpm.sh` publishes without the ohpm CLI (the CLI is on
  neither npmjs nor the Huawei npm mirror); CI job `publish-ohpm` runs it on
  tags.
- **UNVERIFIED on DevEco/device:** the tsc-compiled HAR has never been loaded
  by DevEco Studio or run on a device/emulator. The emitted module is plain ESM
  that keeps the native `import { webSocket } from '@ohos.net.websocket'` /
  `'@ohos.net.http'`, which is what the OHOS runtime resolves - but the first
  `ohpm install etunnel` + build in DevEco is the real acceptance test.

## Verify

```sh
# HAR build (no DevEco): strict tsc compile of Etunnel.ets against the build-time
# @ohos.* stub in types/, then the hvigor HAR layout. Also run by CI publish-ohpm.
make pack-harmonyos
# -> dist/packages/etunnel-harmonyos-<version>.har

# registry dry-run: probe (404 = not published) + PUT with an invalid token.
# Passes when the registry rejects it at the auth layer (HTTP 400
# "The authorization is invalid!"); the publish body itself is only validated
# by the first real publish.
scripts/publish-ohpm.sh dist/packages/etunnel-harmonyos-*.har --dry-run

# full DevEco build (requires DevEco Studio / command-line tools + license)
hvigorw assembleHar

# wire tests under Node (no DevEco): compiles Etunnel.ets against types/ohos-stub.d.ts,
# runs it on Node's WebSocket through the stubs in tests/stubs and checks against
# the Go fixture server that hello carries version and that client_outdated
# rejects with ServerError / stops the supervisor. Needs node >= 22, tsc, go.
sh sdks/harmonyos/tests/run.sh
#   PASS: hello.version == 1.7.39 (fixture: HELLO_VERSION_OK 1.7.39)
#   PASS: client_outdated -> ServerError(code=client_outdated), message verbatim, supervisor stopped
```

`tests/run.sh` was run locally (tsc 6.0.2, Node 26: both cases PASS) - the
first executable check of this SDK's wire behaviour that needs no DevEco toolchain.
`make pack-harmonyos` and the dry-run were run locally by the packaging change (`tsc` 5.5.4: 0 errors; dry-run: HTTP 400 authorization-is-invalid, exit 0). `hvigorw assembleHar` was not run. ArkTS
restrictions honoured in the source: no `any`/`unknown`, no optional-call
syntax, no object spread, explicit interfaces for all wire messages,
`Uint8Array`/`DataView` framing instead of `Buffer`.

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
