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
# SwiftPM consumer (Package.swift):
.package(url: "https://github.com/topcheer/etunnel.git", from: "1.7.36")
# target: .product(name: "etunnel", package: "etunnel")   -> import etunnel

# single file (Foundation only): copy it into your target
# https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/swift/etunnel.swift
# or build the bundled executable from a checkout:
swift run etunnel-cli 3000              # repo root (the root manifest is the SPM package)
cd sdks/swift && swift build -c release     # -> .build/release/etunnel-cli
```

Requires Swift 5.9+ on macOS 13+ / iOS 16+. The repository-root `Package.swift`
exposes the `etunnel` library product (fixed in 1.7.36 - the 1.7.35 root
manifest declared only a broken executable target).

> **If SPM refuses 1.7.36** with `Revision … does not match previously
> recorded value`: the `v1.7.36` tag was re-pointed once after the first
> publish, and SwiftPM caches tag -> revision per machine. Vendor the two
> source files (`etunnel.swift`, `Reconnect.swift`) meanwhile, or depend on
> the next tag (`from: "1.7.37"`, if one is cut) once it exists.

## AI agent quick-start (copy this prompt)

````text
Integrate etunnel (Swift SDK) into this project to expose the local service on port <PORT> at a public HTTPS URL.

- Package: `etunnel` SwiftPM package, git tag (`.package(url: "https://github.com/topcheer/etunnel.git", from: "1.7.36")`, product `etunnel`) · Install: add the package + product, then `import etunnel`; Swift 5.9+, macOS 13+/iOS 16+
- Server: wss://t.etun.uk/tunnel (override with ETUNNEL_SERVER). Anonymous tier = random subdomain, 4h TTL; custom subdomain / longer TTL need a paid token (opts.token or ETUNNEL_TOKEN).
- Minimal embed (verified against the SDK source):
```swift
import Foundation
import etunnel
let tunnel = Etunnel(server: "wss://t.etun.uk/tunnel", local: "3000")!   // nil = malformed server URL
let url = try await tunnel.connect()
print("TUNNEL_URL: " + url)
fflush(stdout)
try await Task.sleep(for: .seconds(3600))
```
- Rules: print the public URL as `TUNNEL_URL: <url>` and keep the task alive (Task.sleep, not Thread.sleep, in async code); call `tunnel.close()` on shutdown to release the subdomain. This SDK is buffer-based (no WebSocket upgrades, no streaming) - fine for webhooks/APIs/dashboards.
- Do not invent other options. Full API/options: #embed and #multi-target-routing. Wire protocol: https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md
````

## CLI

```
swift run etunnel-cli 3000                  # repo root, or from a checkout: cd sdks/swift && swift run etunnel-cli 3000
# or: swift build -c release && .build/release/etunnel-cli 3000
```

(`etunnel.swift` itself has no top-level code, so `swift etunnel.swift 3000`
does nothing; the executable product is `etunnel-cli`.)

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
import Foundation   // or `import etunnel` when using the SwiftPM package

// Single connection (caller owns the retry policy):
let et = Etunnel(server: "wss://t.etun.uk/tunnel", local: "3000")!   // nil = malformed URL
let publicURL = try await et.connect()          // waits for `welcome`
print("TUNNEL_URL: " + publicURL)
fflush(stdout)                                   // stdout is block-buffered when piped
// keep the task alive, e.g. in a script:
try await Task.sleep(for: .seconds(3600))
et.close()                                       // sends `bye`, frees the subdomain
```

`Etunnel(server:local:routes:)` is a failable init (no I/O);
`connect(token:subdomain:resume:) async throws -> String` returns the public URL;
`et.resumeToken` (replay via `resume:`), `et.isClosed`, `close()`.

Supervised tunnel (source-checked; the CLI is built on it) - dials once (failing
fast on bad credentials), then keeps itself alive. Publish the state straight
into your UI.

```swift
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
follow-ups) so the process parks correctly on all versions. Verified on 6.4.2:
a silent exit under a pipe is usually **stdout buffering**, not the toolchain -
`print` into a pipe/log is block-buffered, so add `fflush(stdout)` after printing
the URL (the embed above does).
