# etunnel for HarmonyOS

ohpm package (see "Publishing status" below): `etunnel` (ArkTS, `@ohos.net.websocket`)

Single-file SDK (`Etunnel.ets`): one outbound WebSocket, zero third-party
dependencies. Port of the [TypeScript SDK](../typescript/README.md) to the
ArkTS strict subset — same wire protocol, same #243 hardening limits, same
reconnect semantics. WebSocket (101) pass-through is not forwarded; use the
[Go SDK](../../sdk/README.md) for those.

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

## Platform matrix

| | |
|---|---|
| Language / runtime | ArkTS (HarmonyOS stage model) |
| Transport | `@ohos.net.websocket` (subprotocol `etunnel-v1`) |
| Local replay | `@ohos.net.http` (no `fetch` in ArkTS) |
| Package | HAR via `oh-package.json5` + hvigor config in this directory |
| Dependencies | none (System API only) |
| Body model | buffered per stream (32 KiB chunks) |
| WebSocket (101) pass-through | no — use the [Go SDK](../../sdk/README.md) |
| System API requirements | `@ohos.net.websocket` (API 7+), `http.HttpRequestOptions.protocols`-style subprotocol hints need a recent SDK; a raw `Sec-WebSocket-Protocol` header is sent as well so older stacks still negotiate `etunnel-v1` |

## DevEco integration

Copy `Etunnel.ets` into your module (or install the directory as a local HAR
dependency), then:

```ts
import { connect } from 'etunnel';

// Expose your on-device service (e.g. a local web server) publicly.
let t = await connect('wss://t.etun.uk/tunnel', '3000');
console.log('TUNNEL_URL: ' + t.url);   // https://<name>.t.etun.uk

// Multi-target routing under one public hostname (PROTOCOL.md §2):
let routed = await connect('wss://t.etun.uk/tunnel', '3000', {
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

`connect()` is one-shot; `supervise()` (exported) is the reconnect loop:
exponential backoff 500ms → 30s with ±20% jitter, reset on success, printing
`TUNNEL_URL:` again on every session, and replaying the server-signed
`resumeToken` so the same subdomain is reclaimed instead of re-randomised
(matches every other SDK). For long-lived tunnels run the supervisor from a
long-running task / continuous task so the system does not freeze the
background ability.

### Environment variables

The cross-SDK contract (AGENTS.md) honours the standard variables. HarmonyOS
apps have no process environment, so the parser is a pure function — feed it
a string map (preferences, remote config, a debug launcher) and get identical
behaviour:

```ts
import { configFromEnv, supervise } from 'etunnel';

const cfg = configFromEnv({
  'ETUNNEL_SERVER': 'wss://t.etun.uk/tunnel',
  'ETUNNEL_LOCAL': '8080',
  'ETUNNEL_TOKEN': '<paid-plan api key>',
  'ETUNNEL_SUBDOMAIN': '',        // blank = random
  'ETUNNEL_RECONNECT': '1',       // '0' = single-shot, like every other SDK
});
await supervise(cfg.server, cfg.local, { token: cfg.token, subdomain: cfg.subdomain });
```

| Variable | Meaning | Default |
|---|---|---|
| `ETUNNEL_SERVER` | tunnel server WebSocket URL | `wss://t.etun.uk/tunnel` |
| `ETUNNEL_LOCAL` | local port or `host:port` | `3000` |
| `ETUNNEL_TOKEN` | paid-plan API key | - |
| `ETUNNEL_SUBDOMAIN` | requested subdomain (paid plans) | random |
| `ETUNNEL_RECONNECT` | `0` = single-shot, anything else = reconnect loop | reconnect |

There is no CLI binary and no `login()` loopback OAuth on this platform
(no local HTTP server / `open` in an app sandbox) — obtain the API key from
the portal at `/me` in a browser and pass it as `ETUNNEL_TOKEN` / `opts.token`.

## Publishing status (ohpm)

**Pending: Huawei developer account + app/package review.** Publishing to
ohpm (the HarmonyOS registry) requires an open-source developer account and
package review; until that lands, install from source:

```sh
# in your project root
ohpm install ./path/to/etunnel-harmonyos
# or reference the directory in oh-package.json5 dependencies
```

This is a documented follow-up, not a blocker for the SDK itself.

## CI toolchain limitation

GitHub Actions has **no official DevEco Studio / hvigor toolchain image**, and
the HarmonyOS command-line build (hvigor + SDK download) requires accepting
Huawei licenses against developer accounts — there is nothing to wire a
`publish-harmonyos` CI job to today. As a result:

- CI builds **do not** package the HAR (no `pack-harmonyos` in the workflow;
  do not add one until an official/licensed toolchain image exists).
- Verification here is `tsc --strict` over the SDK source against `@ohos`
  type stubs plus runtime unit tests of the pure logic (route tables,
  `ETUNNEL_*` mapping, UTF-8 framing helpers) — see the PR description.
- DevEco Studio users: open this directory as a HAR module and run
  `hvigorw assembleHar` to build `etunnel-1.4.0.har` locally.

## Verify

```sh
# static (no DevEco needed): strict type check + pure-logic tests
tsc --strict --noEmit --target ES2020 sdks/harmonyos/Etunnel.ets   # with @ohos stubs
# full DevEco build (requires DevEco Studio / command-line tools + license)
hvigorw assembleHar
```

ArkTS restrictions honoured in the source: no `any`/`unknown`, no optional-call
syntax, no object spread, explicit interfaces for all wire messages,
`Uint8Array`/`DataView` framing instead of `Buffer`.

## Limits

- Buffered bodies per stream (32 KiB frames): right for webhooks, JSON APIs,
  dashboards; not for large uploads, SSE, or WebSocket pass-through.
- One WebSocket per tunnel; concurrency is single-threaded on the ArkTS event
  loop, like the Node/TS/Deno SDKs.

## License

MIT — see [LICENSE](../../LICENSE).
