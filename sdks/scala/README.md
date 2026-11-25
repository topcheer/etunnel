# etunnel for Scala

Single-file SDK (`Etunnel.scala`) on the JDK HttpClient/WebSocket — zero dependencies beyond the Scala standard library.

**Published on Maven Central, cross-built:** `dev.ggcode:etunnel_2.13` and
`dev.ggcode:etunnel_3` (package `dev.ggcode.etunnel`; with sbt just write
`"dev.ggcode" %% "etunnel"`). Or copy the single file into your project - it
declares `package dev.ggcode.etunnel`, so import from there. The Maven Central
artifact `dev.ggcode:etunnel` without a suffix is the [Java SDK](../java/README.md).

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

## Install

sbt (`%%` picks `etunnel_2.13` or `etunnel_3` from your `scalaVersion`):

```scala
libraryDependencies += "dev.ggcode" %% "etunnel" % "1.7.37"
```

Maven (Scala 3 shown; for 2.13 use `etunnel_2.13`):

```xml
<dependency>
  <groupId>dev.ggcode</groupId>
  <artifactId>etunnel_3</artifactId>
  <version>1.7.37</version>
</dependency>
```

Or run the single file without a build tool:

```sh
curl -O https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/scala/Etunnel.scala
```

Add `Etunnel.scala` to your sbt project's `src/main/scala`, or run it directly
with the Scala runner (verified with Scala code runner 1.19.0 / Scala 3.10.1,
JDK 17). Requires JDK 11+ (`java.net.http.WebSocket`).

## AI agent quick-start (copy this prompt)

```text
Integrate etunnel (Scala SDK) into this project to expose the local service on port 3000 at a public HTTPS URL.

- Package: dev.ggcode:etunnel_2.13 / dev.ggcode:etunnel_3 on Maven Central (Scala 2.13 and 3 cross-build, package dev.ggcode.etunnel; JDK 11+) · Install sbt: libraryDependencies += "dev.ggcode" %% "etunnel" % "1.7.37" · Install Maven: <dependency><groupId>dev.ggcode</groupId><artifactId>etunnel_3</artifactId><version>1.7.37</version></dependency> (etunnel_2.13 for Scala 2.13) · Or vendor sdks/scala/Etunnel.scala (it declares package dev.ggcode.etunnel). Standalone CLI: scala Etunnel.scala -- 3000 (Scala 3 runner; the `--` separates program arguments)
- Server: wss://t.etun.uk/tunnel (override with ETUNNEL_SERVER). Anonymous tier = random subdomain, 4h TTL; custom subdomain / longer TTL need a paid token (token = "..." or ETUNNEL_TOKEN).
- Minimal embed (verified against the SDK source and run against a live server):
import dev.ggcode.etunnel.Etunnel

object Main {
  def main(args: Array[String]): Unit = {
    val t = Etunnel.connect("wss://t.etun.uk/tunnel", 3000)
    println("TUNNEL_URL: " + t.url)
    Thread.currentThread().join()
  }
}
- Rules: print the public URL as `TUNNEL_URL: <url>` and keep the process alive. This SDK has no close() method: the tunnel lives as long as the JVM (or close the socket with Etunnel.lastWs.sendClose(java.net.http.WebSocket.NORMAL_CLOSURE, "bye")). It is buffer-based (no WebSocket upgrades, no streaming) - fine for webhooks/APIs/dashboards. Named arguments: Etunnel.connect(server, 3000, token = "...", subdomain = "myapp", routes = Seq(Etunnel.Route("/api", "127.0.0.1:8080", strip = true))).
- Do not invent other options. Full API/options: https://github.com/topcheer/etunnel/blob/main/sdks/scala/README.md#api and #multi-target-routing. Wire protocol: https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md
```

## CLI

```sh
scala Etunnel.scala -- 3000
```

On the Scala 3 code runner (1.19.0, verified) program arguments go after
`--`; `scala Etunnel.scala 3000` fails there with `3000: input file not
found`. (The classic Scala 2.13 runner accepted `scala Etunnel.scala 3000`.)

Prints `TUNNEL_URL: https://<subdomain>.t.etun.uk` — that URL now routes to
your local service. Ctrl-C closes the tunnel. The first argument is a port
(`ETUNNEL_LOCAL` accepts a port or `127.0.0.1:port` / `localhost:port`).

**Reconnects automatically.** If the connection drops (including a server
restart) the CLI re-dials with exponential backoff (500ms → 30s, ±20%
jitter, reset on success) and prints `TUNNEL_URL:` again. The subdomain is
**reclaimed**, not re-randomised: the client replays the server's signed
`resumeToken` in `hello.resume`, so links already shared keep working (the
server needs a stable `SESSION_SECRET`; see PROTOCOL.md §1). Set
`ETUNNEL_RECONNECT=0` to restore the old one-shot behaviour (verified on the
1.7.36 source: `ETUNNEL_RECONNECT=0 scala Etunnel.scala -- 3000` prints
exactly `TUNNEL_URL: <url>`; 1.7.35 printed `Tunnel(<url>,<resumeToken>)`
there instead).

## Embed

```scala
import dev.ggcode.etunnel.Etunnel

object Main {
  def main(args: Array[String]): Unit = {
    val t = Etunnel.connect("wss://t.etun.uk/tunnel", 3000)
    println("TUNNEL_URL: " + t.url)
    Thread.currentThread().join()
  }
}
```

Run against a live server as `scala -M Main Etunnel.scala Main.scala`
(the runner compiles both files; `-M` picks the main object). `scalac
Etunnel.scala Main.scala -d out` also compiles cleanly if you prefer a build
tool. With the Maven Central artifacts the same `Main` ran from sbt
(`libraryDependencies += "dev.ggcode" %% "etunnel" % "1.7.37"`, scalaVersion
3.3.9 -> `etunnel_3` and 2.13.18 -> `etunnel_2.13`) and from a Maven
`scala-maven-plugin` project - each printed the URL and the probe through the
tunnel returned the local service.

## API

| Symbol | Returns / effect |
|---|---|
| `Etunnel.connect(server, localPort, token = null, subdomain = null, resume = null, routes = null)` | `Tunnel` (blocking until `welcome`; `localPort` may be `0` when `routes` is set) |
| `Tunnel.url` / `.resumeToken` | case-class fields from the `welcome` frame (pass `resumeToken` back as `resume` to reclaim the subdomain) |
| `Etunnel.lastWs` | the live `java.net.http.WebSocket` (there is no `close()`; `sendClose(...)` on it releases the subdomain) |
| `Etunnel.Route(prefix, addr, strip = false)` | one route row for `routes: Seq[Route]` |
| `Etunnel.normalizeRoutes(Seq[Route])` / `Etunnel.routeFor(Array[Route], pathAndQuery)` | the route-table helpers the tunnel itself uses |

Options: default arguments keep call sites short - `Etunnel.connect(server, 3000, subdomain = "myapp")`.

## Environment variables

| Variable | Meaning | Default |
|---|---|---|
| `ETUNNEL_SERVER` | tunnel server WebSocket URL | `wss://t.etun.uk/tunnel` |
| `ETUNNEL_LOCAL` | local port or `127.0.0.1:port` (or 1st CLI arg) | `3000` |
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

```scala
import dev.ggcode.etunnel.Etunnel

val t = Etunnel.connect("wss://t.example.com/tunnel", 0, routes = Seq(
  Etunnel.Route("/api", "127.0.0.1:8080", strip = true),
  Etunnel.Route("/", "127.0.0.1:3000")))
```

Semantics (identical in every SDK, pinned by tests): longest prefix
wins; matching is on segment boundaries (`/api` never matches `/apiv2`);
`strip` removes only the path part - the query string is carried over
byte-for-byte, an exact hit bottoms out at `/`; no matching route (only
possible without a `/` fallback) answers `404` at the SDK edge.
Verified against a live server: `/api/items?id=7` reached the local service
as `/items?id=7`, `/other` took the `/` route unchanged.

> Redirect note: with `strip`, a backend 302 to an absolute path (`/users/next`) lands on the fallback route on the next hop - backends should return relative redirects or keep the prefix (same as nginx `proxy_pass` with trailing slash; see #155).
