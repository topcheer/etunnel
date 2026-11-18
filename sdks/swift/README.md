# etunnel for Swift

[![SwiftPM](https://img.shields.io/badge/SwiftPM-compatible-orange?logo=swift)](https://img.shields.io/badge/platforms-macOS%20%2B%20iOS-blue)](https://github.com/topcheer/etunnel)

Foundation-only single-file SDK (URLSession WebSocket).

> **Apple platforms only.** The SDK builds on `URLSessionWebSocketTask`,
> which the Linux swift-corelibs-foundation has never implemented - `swift
> build` on Linux cannot work (#90). Compile verification runs on the macOS
> CI job; Linux users, see the other 19 SDKs.

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

## Install

```
swift build   # SPM: .package(url:..., from: "1.0.0")
```

Requires Swift 5.9+ on macOS 13+ / iOS 16+.

## CLI

```
swift etunnel.swift 3000
```

Prints `TUNNEL_URL: https://<subdomain>.t.etun.uk` — that URL now routes to
your local service. Ctrl-C closes the tunnel.

**Reconnects automatically.** If the connection drops (including a server
restart) the CLI re-dials with exponential backoff (500ms → 30s, ±10%
jitter, reset on success) and prints `TUNNEL_URL:` again. The subdomain is
**reclaimed**, not re-randomised: the client replays the server's signed
`resumeToken` in `hello.resume`, so links already shared keep working (the
server needs a stable `SESSION_SECRET`; see PROTOCOL.md §1). Set
`ETUNNEL_RECONNECT=0` to restore the old one-shot behaviour.

## Embed

```swift
import etunnel

// Supervised tunnel: dials once (failing fast on bad credentials), then keeps
// itself alive. Publish the state straight into your UI.
guard let tunnel = ReconnectingEtunnel(server: "wss://t.etun.uk/tunnel", local: "3000") else {
    return  // invalid URL / route table
}
tunnel.onState = { state in
    switch state {
    case .connecting:                                   break
    case .connected(let url):                           print("up:", url)
    case .retrying(let attempt, let delay, let url):    print("retry \(attempt) in \(delay)s (last: \(url ?? "-"))")
    case .closed(let reason):                           print("stopped:", reason)
    }
}
let url = try await tunnel.connect()          // first public URL
// ... later
tunnel.close()                                 // never reconnects

// Or consume transitions as an AsyncStream:
for await state in tunnel.states { /* render */ }

// Single connection (caller owns the retry policy):
let et = Etunnel(server: url, local: "3000")!
let publicURL = try await et.connect(token: nil, subdomain: nil, resume: nil)
```

**Reconnect semantics** (identical to the Go SDK's `ConnectReconnecting`, so
both SDKs behave the same):

| | |
|---|---|
| first dial | fails fast - bad token/plan does not loop forever |
| drop not requested by you | retried with exponential backoff 0.5s → 30s, ×2, ±10% jitter |
| backoff | resets after a successful reconnect |
| subdomain | reclaimed via the server-signed resume token; `publicURL` keeps showing the last known URL while retrying |
| `close()` | stops the supervisor for good |
| permanent errors | `unauthorized`, `subdomain_plan`, `subdomain_invalid`, `bad_message`, protocol deviation: stop instead of retrying |
| policy override | `EtunnelReconnectPolicy(minBackoff:maxBackoff:factor:jitterFraction:maxAttempts:)` |

`onReconnect(attempt, url)` fires after each successful reconnect. State
delivery is done on the main actor, so assigning to a `@Published` property is
safe.

E2E: `./Tests/test_reconnect.sh` hard-kills a real server mid-connection,
restarts it, and asserts the **same** public URL comes back.

## Environment variables

| Variable | Meaning | Default |
|---|---|---|
| `ETUNNEL_SERVER` | tunnel server WebSocket URL | `wss://t.etun.uk/tunnel` |
| `ETUNNEL_LOCAL` | local port or `host:port` (or 1st CLI arg) | `3000` |
| `ETUNNEL_TOKEN` | paid-plan API key (optional) | - |
| `ETUNNEL_RECONNECT` | `0` disables auto-reconnect (one-shot CLI) | enabled |
| `SESSION_SECRET` | server-side: stable key for resume tokens (required across restarts) | per-process random |
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

One public hostname, several local services - e.g. `/` to a built frontend
and `/api` to a backend - without CORS or a second tunnel. Routing is
client-side; the protocol is unchanged (PROTOCOL.md 2).

```swift
let t = Etunnel(server: "wss://t.example.com/tunnel", local: "3000", routes: [
    EtunnelRoute(prefix: "/api", addr: "127.0.0.1:8080", strip: true),
    EtunnelRoute(prefix: "/", addr: "127.0.0.1:3000"),
])
```

Semantics (identical in every SDK, pinned by tests): longest prefix
wins; matching is on segment boundaries (`/api` never matches `/apiv2`);
`strip` removes only the path part - the query string is carried over
byte-for-byte, an exact hit bottoms out at `/`; no matching route (only
possible without a `/` fallback) answers `404` at the SDK edge.

> Redirect note: with `strip`, a backend 302 to an absolute path (`/users/next`) lands on the fallback route on the next hop - backends should return relative redirects or keep the prefix (same as nginx `proxy_pass` with trailing slash; see #155).

## Toolchain note

The example is written for Swift 5.9+ script semantics (`swift` interpreter,
per the header comment in example/basic.swift). Newer toolchains (6.4+)
changed top-level-async execution; if the concatenated script exits
silently under your toolchain, wrap the example body in a `@main` struct
with an async `main()` (the library itself builds clean on 6.x:
`swift build` is the gate). The demo keeps `dispatchMain()` (#149-152
follow-ups) so the process parks correctly on all versions.
