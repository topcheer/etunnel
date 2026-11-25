# etunnel for Kotlin

Single-file SDK (`Etunnel.kt`) on the JDK HttpClient/WebSocket — zero dependencies, no Kotlin library beyond the stdlib.

**Published as `dev.ggcode:etunnel-kotlin` on Maven Central** (package
`dev.ggcode.etunnel`). Use it as a Gradle/Maven dependency, or copy the single
file into your project (`Etunnel.kt` declares `package dev.ggcode.etunnel`, so
import from there). The Maven Central artifact `dev.ggcode:etunnel` is the
[Java SDK](../java/README.md); `dev.ggcode:etunnel-android` the [Android AAR](../android/README.md).

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

## Install

Gradle (Kotlin DSL) — verified with Kotlin 2.1.0 / Gradle against a live server:

```kotlin
repositories { mavenCentral() }
dependencies {
    implementation("dev.ggcode:etunnel-kotlin:1.7.37")   // pulls kotlin-stdlib 2.1.0
}
```

Maven:

```xml
<dependency>
  <groupId>dev.ggcode</groupId>
  <artifactId>etunnel-kotlin</artifactId>
  <version>1.7.37</version>
</dependency>
```

Or vendor the single file (no build tool):

```sh
curl -O https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/kotlin/Etunnel.kt
```

Then either drop `Etunnel.kt` into your Gradle project (`src/main/kotlin`),
or build the standalone CLI jar (verified with kotlinc 2.4.21 on JDK 17):

```sh
kotlinc Etunnel.kt -include-runtime -d etunnel.jar
```

Requires JDK 11+ (uses `java.net.http.WebSocket`; no `kotlinx` libraries).

## AI agent quick-start (copy this prompt)

```text
Integrate etunnel (Kotlin SDK) into this project to expose the local service on port 3000 at a public HTTPS URL.

- Package: dev.ggcode:etunnel-kotlin on Maven Central (Kotlin, package dev.ggcode.etunnel; pulls kotlin-stdlib 2.1.0; JDK 11+) · Install Gradle: implementation("dev.ggcode:etunnel-kotlin:1.7.37") · Install Maven: <dependency><groupId>dev.ggcode</groupId><artifactId>etunnel-kotlin</artifactId><version>1.7.37</version></dependency> · Or vendor sdks/kotlin/Etunnel.kt (it declares package dev.ggcode.etunnel). Standalone CLI: kotlinc Etunnel.kt -include-runtime -d etunnel.jar && java -jar etunnel.jar 3000
- Server: wss://t.etun.uk/tunnel (override with ETUNNEL_SERVER). Anonymous tier = random subdomain, 4h TTL; custom subdomain / longer TTL need a paid token (token = ... or ETUNNEL_TOKEN).
- Minimal embed (verified against the SDK source and run against a live server):
import dev.ggcode.etunnel.Etunnel

fun main() {
    val tunnel = Etunnel.connect("wss://t.etun.uk/tunnel", 3000)
    println("TUNNEL_URL: " + tunnel.url)

    Runtime.getRuntime().addShutdownHook(Thread { tunnel.close() })
    Thread.currentThread().join()
}
- Rules: print the public URL as `TUNNEL_URL: <url>` and keep the process alive; call tunnel.close() on shutdown to release the subdomain. This SDK is buffer-based (no WebSocket upgrades, no streaming) - fine for webhooks/APIs/dashboards. Named arguments: Etunnel.connect(server, 3000, token = "...", subdomain = "myapp", routes = listOf(Route("/api", "127.0.0.1:8080", strip = true))).
- Do not invent other options. Full API/options: https://github.com/topcheer/etunnel/blob/main/sdks/kotlin/README.md#api and #multi-target-routing. Wire protocol: https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md
```

## CLI

```sh
java -jar etunnel.jar 3000
```

Prints `TUNNEL_URL: https://<subdomain>.t.etun.uk` — that URL now routes to
your local service. Ctrl-C closes the tunnel. The first argument is a
**port number only** (`ETUNNEL_LOCAL` accepts a port too; `host:port` is
not parsed by this CLI).

**Reconnects automatically.** If the connection drops (including a server
restart) the CLI re-dials with exponential backoff (500ms → 30s, ±20%
jitter, reset on success) and prints `TUNNEL_URL:` again. The subdomain is
**reclaimed**, not re-randomised: the client replays the server's signed
`resumeToken` in `hello.resume`, so links already shared keep working (the
server needs a stable `SESSION_SECRET`; see PROTOCOL.md §1). Set
`ETUNNEL_RECONNECT=0` to restore the old one-shot behaviour.

## Embed

```kotlin
import dev.ggcode.etunnel.Etunnel

fun main() {
    val tunnel = Etunnel.connect("wss://t.etun.uk/tunnel", 3000)
    println("TUNNEL_URL: " + tunnel.url)

    Runtime.getRuntime().addShutdownHook(Thread { tunnel.close() })
    Thread.currentThread().join()
}
```

Compiled with `kotlinc Etunnel.kt App.kt -include-runtime -d app.jar` and run
as `java -cp app.jar AppKt` against a live server; the printed URL served
the local service. With the Maven Central artifact the same `main` ran as a
Gradle `implementation("dev.ggcode:etunnel-kotlin:1.7.37")` project (Kotlin 2.1.0)
and as a Maven `kotlin-maven-plugin` project - both printed the URL and the
probe through the tunnel returned the local service.

## API

| Symbol | Returns / effect |
|---|---|
| `Etunnel.connect(server, localPort, token = null, subdomain = null, resume = null, routes = null)` | `Etunnel` (blocking until `welcome`; `localPort` may be `0` when `routes` is set) |
| `Etunnel.url` / `.resumeToken` | public properties from the `welcome` frame (pass `resumeToken` back as `resume` to reclaim the subdomain) |
| `Etunnel.close()` | sends `bye` + a WebSocket close, releasing the subdomain |
| `Route(prefix, addr, strip = false)` | one route row for `routes` |
| `Etunnel.normalizeRoutes(List<Route>)` / `Etunnel.routeFor(List<Route>, pathAndQuery)` | the route-table helpers the tunnel itself uses |

Options: named arguments on `connect` - `Etunnel.connect(server, 3000, token = "et_...", subdomain = "myapp")`.

## Environment variables

| Variable | Meaning | Default |
|---|---|---|
| `ETUNNEL_SERVER` | tunnel server WebSocket URL | `wss://t.etun.uk/tunnel` |
| `ETUNNEL_LOCAL` | local port (or 1st CLI arg; port only) | `3000` |
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

One public hostname, several local services - e.g. `/` to a built frontend
and `/api` to a backend - without CORS or a second tunnel. Routing is
client-side; the protocol is unchanged (PROTOCOL.md 2).

```kotlin
import dev.ggcode.etunnel.Etunnel
import dev.ggcode.etunnel.Route

val t = Etunnel.connect("wss://t.example.com/tunnel", 0, routes = listOf(
    Route("/api", "127.0.0.1:8080", strip = true),
    Route("/", "127.0.0.1:3000"),
))
```

Semantics (identical in every SDK, pinned by tests): longest prefix
wins; matching is on segment boundaries (`/api` never matches `/apiv2`);
`strip` removes only the path part - the query string is carried over
byte-for-byte, an exact hit bottoms out at `/`; no matching route (only
possible without a `/` fallback) answers `404` at the SDK edge.
Verified against a live server: `/api/items?id=7` reached the local service
as `/items?id=7`, `/other` took the `/` route unchanged.

> Redirect note: with `strip`, a backend 302 to an absolute path (`/users/next`) lands on the fallback route on the next hop - backends should return relative redirects or keep the prefix (same as nginx `proxy_pass` with trailing slash; see #155).
