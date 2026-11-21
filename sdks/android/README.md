# etunnel for Android

Maven Central: `dev.ggcode:etunnel-android` (AAR, Kotlin + OkHttp3)

Single-file SDK (`src/main/kotlin/Etunnel.kt`): one outbound OkHttp3
WebSocket, zero Android-specific dependencies beyond OkHttp. Sibling of the
[Kotlin SDK](../kotlin/README.md) (same wire protocol, JVM HttpClient) —
on Android `java.net.http` does not exist, so this variant speaks WebSocket
through OkHttp3 and HTTP through the same OkHttp client.

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

## Platform matrix

| | |
|---|---|
| Language / runtime | Kotlin (compiled to JVM bytecode, D8-desugared) |
| Min SDK | 24 (Android 7.0) |
| Compile SDK | 35 |
| Artifact | `dev.ggcode:etunnel-android:1.7.26` (AAR) |
| Transport | OkHttp3 4.12 WebSocket (`etunnel-v1` subprotocol) |
| Body model | buffered per stream (32 KiB chunks) |
| WebSocket (101) pass-through | no — use the [Go SDK](../../sdk/README.md) for that |
| Dependency | OkHttp only (pulled transitively by most apps already) |

## Install

```kotlin
// build.gradle.kts of your app module
dependencies {
    implementation("dev.ggcode:etunnel-android:1.7.26")
}
```

Or copy the single file `src/main/kotlin/Etunnel.kt` into your project —
it has no Android framework imports, only Kotlin + OkHttp.

## Use

```kotlin
import dev.ggcode.etunnel.Etunnel
import dev.ggcode.etunnel.Route

// Expose your app's localhost:8080 (e.g. an embedded web server) publicly.
val tunnel = Etunnel.connect(
    server = "wss://t.etun.uk/tunnel",  // or your own etunneld
    localPort = 8080,
    token = null,                       // paid-plan API key from the /me portal
    subdomain = null,                   // custom subdomain (paid plans)
)
Log.i("etunnel", "TUNNEL_URL: ${tunnel.url}")   // https://<name>.t.etun.uk

// Optional: route by path prefix to different local ports (PROTOCOL.md §2).
val routed = Etunnel.connect(
    "wss://t.etun.uk/tunnel", 3000, routes = listOf(
        Route("/api", "127.0.0.1:8080"),
        Route("/admin", "127.0.0.1:9090", strip = true),
    ))

tunnel.close()   // sends `bye` and releases the socket
```

Keep the returned `tunnel.url` around: on reconnect the SDK replays the
server-signed `resumeToken`, so the same subdomain is reclaimed instead of
re-randomised (matches every other SDK).

### Reconnect loop

`Etunnel.connect` is one-shot. For a long-lived tunnel, wrap it in the same
supervise-retry pattern the bundled CLI `main()` uses: reconnect with
exponential backoff (500ms → 30s, ±20% jitter, reset on success) and pass
the previous `resumeToken` on every retry.

### Environment variables

The cross-SDK contract (AGENTS.md) honours the standard variables. On
Android, process env vars are rarely populated, so the parser is exposed as
a pure function — feed it your own map (BuildConfig fields, SharedPreferences,
`ProcessBuilder`-style launchers) and get the same behaviour as the CLI:

```kotlin
val cfg = configFromEnv(System.getenv())        // desktop/JVM parity
val cfg2 = configFromEnv(                       // Android-typical
    mapOf("ETUNNEL_SERVER" to "wss://t.etun.uk/tunnel",
          "ETUNNEL_LOCAL" to "8080",
          "ETUNNEL_TOKEN" to buildConfigFieldToken))
Etunnel.connect(cfg2.server, cfg2.localPort, cfg2.token, cfg2.subdomain)
```

| Variable | Meaning | Default |
|---|---|---|
| `ETUNNEL_SERVER` | tunnel server WebSocket URL | `wss://t.etun.uk/tunnel` |
| `ETUNNEL_LOCAL` | local port | `3000` |
| `ETUNNEL_TOKEN` | paid-plan API key | - |
| `ETUNNEL_SUBDOMAIN` | requested subdomain (paid plans) | random |
| `ETUNNEL_RECONNECT` | `0` = single-shot, anything else = reconnect loop | reconnect |

## Cleartext to localhost (Android 9+)

The tunnel replays requests to your local service over **plain HTTP**
(`http://127.0.0.1:<port>`). Since `targetSdk >= 28`, Android blocks all
cleartext traffic by default — **including loopback** — so on a modern host
app every replay fails with `Cleartext HTTP traffic to 127.0.0.1 not
permitted` and the visitor sees a 502 page. The SDK logs a targeted hint the
first time this happens (see `Etunnel.onLog` below).

Pick one app-side fix:

**Preferred — network-security-config scoped to loopback.**

`res/xml/network_security_config.xml`:

```xml
<network-security-config>
    <base-config cleartextTrafficPermitted="false" />
    <domain-config cleartextTrafficPermitted="true">
        <domain includeSubdomains="false">127.0.0.1</domain>
        <domain includeSubdomains="false">localhost</domain>
        <domain includeSubdomains="false">::1</domain>
    </domain-config>
</network-security-config>
```

and reference it in your `AndroidManifest.xml`:

```xml
<application android:networkSecurityConfig="@xml/network_security_config" ... >
```

Note: `<domain-config>` entries match hostnames; some Android versions do not
match raw IP literals against `<domain>` entries. If replays still fail with
this config, use the app-wide fallback below.

**Fallback — allow cleartext app-wide** (blunt; only for apps whose security
posture already permits it):

```xml
<application android:usesCleartextTraffic="true" ... >
```

**Detect it at runtime** — wire the SDK's log hook early; it emits a targeted
hint the first time a replay is blocked:

```kotlin
Etunnel.onLog = { Log.w("etunnel", it) }
```

## Doze and foreground services

Android aggressively suspends apps in the background, which will sever or
starve a long-lived tunnel. Practical guidance:

- **Run the tunnel from a foreground service** (`ForegroundService`) with a
  persistent notification. A tunnel that must survive screen-off needs to be
  a foreground service; a plain `Service` or coroutine in an Activity will be
  frozen by Doze within minutes.
  - On API 34+ declare the type in the manifest and at start:
    `android:foregroundServiceType="dataSync"` and
    `startForeground(id, notification, FOREGROUND_SERVICE_TYPE_DATA_SYNC)`.
    Note Android 15 (API 35) caps `dataSync` foreground services at ~6 hours
    per day — if your tunnel is permanent, prefer `connectedDevice` (paired
    hardware scenario) or `specialUse` with a documented justification.
  - You need `FOREGROUND_SERVICE` (normal) and, on API 34+,
    `FOREGROUND_SERVICE_DATA_SYNC` (normal) permissions; the runtime
    notification permission `POST_NOTIFICATIONS` on API 33+ so users see the
    tunnel is alive.
- **Reconnect, don't assume.** Mobile networks change (WiFi ↔ 5G), so treat
  every `onClosed`/`onFailure` as expected: backoff-retry with `resumeToken`.
  The OkHttp client already sends 30-second WS protocol pings to keep NAT
  mappings warm; the server's own `ping`/`pong` messages keep application
  state honest.
- **Battery optimization exemption** (`REQUEST_IGNORE_BATTERY_OPTIMIZATIONS`)
  is only appropriate for user-facing tunnel apps that the user explicitly
  wants always-on; Play policy restricts it, so ask, don't assume.
- **WorkManager is the wrong tool** for the socket itself (15-minute minimum
  granularity, no persistent connections). It is fine for *coordinating*
  periodic re-dials of a tunnel that doesn't need to be always-on.
- **App standby / buckets**: a foreground service keeps the app in the active
  bucket; without one, expect the socket to be killed and design the resume
  path to be invisible to the user.

## Limits

- Buffered bodies per stream (32 KiB frames): right for webhooks, JSON APIs,
  dashboards; not for large uploads, SSE, or WebSocket pass-through — use the
  Go SDK (`Connect(ctx, Options{})`) where you need streaming.
- One WebSocket per tunnel; concurrency is served sequentially per stream by
  OkHttp's dispatcher, like the Java/Kotlin/Clojure SDKs.
- Local upstream requests are always direct to `127.0.0.1:<port>` (OkHttp
  `Proxy.NO_PROXY`) — a device-wide HTTP proxy will never intercept the
  tunnel's own replays.

## Verify

```sh
cd sdks/android
./gradlew build     # AAR (assembleRelease) + pure-JVM unit tests
```

The tests run on the JVM (no emulator): ETUNNEL_* env mapping, JSON control
messages, route tables and the 9-byte binary frame layout.

## License

MIT — see [LICENSE](../../LICENSE).
