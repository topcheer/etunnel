# etunnel for Java

[![Maven Central](https://img.shields.io/maven-central/v/dev.ggcode/etunnel?label=Maven%20Central)](https://central.sonatype.com/artifact/dev.ggcode/etunnel)
[![JDK](https://img.shields.io/badge/JDK-11%2B-orange)](https://central.sonatype.com/artifact/dev.ggcode/etunnel)

Single-file SDK, JDK 11+ built-in HttpClient/WebSocket — zero dependencies. Published to Maven Central as `dev.ggcode:etunnel`.

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

## Install

Maven (`pom.xml`):

```xml
<dependency>
  <groupId>dev.ggcode</groupId>
  <artifactId>etunnel</artifactId>
  <version>1.7.36</version>
</dependency>
```

Gradle (`build.gradle`):

```groovy
dependencies {
    implementation "dev.ggcode:etunnel:1.7.36"
}
```

Both coordinates were resolved from Maven Central and run against a live
etunneld (Maven 3.10.1 and Gradle 9.9.2, JDK 17). `1.7.36` is the release
this README was verified against - check the badge above for newer versions.

Single-file alternative: copy `src/main/java/dev/ggcode/Etunnel.java` into any
JDK 11+ project (package `dev.ggcode`); it has no dependencies.

Requires JDK 11+ (`java.net.http.WebSocket`); verified on JDK 17.

## AI agent quick-start (copy this prompt)

```text
Integrate etunnel (Java SDK) into this project to expose the local service on port 3000 at a public HTTPS URL.

- Package: dev.ggcode:etunnel on Maven Central · Install: Maven <dependency><groupId>dev.ggcode</groupId><artifactId>etunnel</artifactId><version>1.7.36</version></dependency> or Gradle implementation "dev.ggcode:etunnel:1.7.36" (JDK 11+, zero transitive dependencies).
- Server: wss://t.etun.uk/tunnel (override with ETUNNEL_SERVER). Anonymous tier = random subdomain, 4h TTL; custom subdomain / longer TTL need a paid token (Options.token or ETUNNEL_TOKEN).
- Minimal embed (verified against the published 1.7.36 artifact and run against a live server):
import dev.ggcode.Etunnel;

public class Main {
    public static void main(String[] args) throws Exception {
        Etunnel.Tunnel tunnel = Etunnel.connect("wss://t.etun.uk/tunnel", 3000, null);
        System.out.println("TUNNEL_URL: " + tunnel.url);

        Runtime.getRuntime().addShutdownHook(new Thread(tunnel::close));
        Thread.currentThread().join();   // keep the tunnel open
    }
}
- Rules: print the public URL as `TUNNEL_URL: <url>` and keep the process alive; call tunnel.close() on shutdown to release the subdomain. This SDK is buffer-based (no WebSocket upgrades, no streaming) - fine for webhooks/APIs/dashboards. Etunnel.Options has public fields (token, subdomain, resume, routes; no fluent setters); pass null for an anonymous tunnel.
- Do not invent other options. Full API/options: https://github.com/topcheer/etunnel/blob/main/sdks/java/README.md#api and #multi-target-routing. Wire protocol: https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md
```

## CLI

```sh
java src/main/java/dev/ggcode/Etunnel.java 3000   # from sdks/java, single-file launch, JDK 11+
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

```java
import dev.ggcode.Etunnel;

public class Main {
    public static void main(String[] args) throws Exception {
        Etunnel.Tunnel tunnel = Etunnel.connect("wss://t.etun.uk/tunnel", 3000, null);
        System.out.println("TUNNEL_URL: " + tunnel.url);

        Runtime.getRuntime().addShutdownHook(new Thread(tunnel::close));
        Thread.currentThread().join();   // keep the tunnel open
    }
}
```

`connect` blocks until the server's `welcome`, so `tunnel.url` is set when it
returns. Same code works for Maven/Gradle projects and for the single-file
copy.

## API

| Symbol | Returns / effect |
|---|---|
| `Etunnel.connect(server, localPort, Options)` | `Tunnel` (blocking until `welcome`; `localPort` may be `0` when `Options.routes` is set) |
| `Tunnel.url` / `.hostname` / `.subdomain` / `.plan` / `.resumeToken` | public final fields from the `welcome` frame |
| `Tunnel.close()` | sends `bye` + a WebSocket close, releasing the subdomain |
| `Etunnel.normalizeRoutes(Route[])` / `Etunnel.routeFor(Route[], pathAndQuery)` | the route-table helpers the tunnel itself uses |

Options: `Etunnel.Options` has **public fields** `token`, `subdomain`,
`resume`, `routes` (there are no fluent setters) - or pass `null` for an
anonymous tunnel:

```java
Etunnel.Options o = new Etunnel.Options();
o.token = "et_...";
o.subdomain = "myapp";
Etunnel.Tunnel t = Etunnel.connect("wss://t.etun.uk/tunnel", 3000, o);
```

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

One public hostname, several local services - e.g. `/` to a built frontend
and `/api` to a backend - without CORS or a second tunnel. Routing is
client-side; the protocol is unchanged (PROTOCOL.md 2).

```java
Tunnel t = Etunnel.connect("wss://t.example.com/tunnel", 0, new Etunnel.Options() {{
    routes = new Etunnel.Route[] {
        new Etunnel.Route() {{ prefix = "/api"; addr = "127.0.0.1:8080"; strip = true; }},
        new Etunnel.Route() {{ prefix = "/";    addr = "127.0.0.1:3000"; }},
    };
}});
```

Semantics (identical in every SDK, pinned by tests): longest prefix
wins; matching is on segment boundaries (`/api` never matches `/apiv2`);
`strip` removes only the path part - the query string is carried over
byte-for-byte, an exact hit bottoms out at `/`; no matching route (only
possible without a `/` fallback) answers `404` at the SDK edge.
Verified against a live server: `/api/items?id=7` reached the local service
as `/items?id=7`, `/other` took the `/` route unchanged.

> Redirect note: with `strip`, a backend 302 to an absolute path (`/users/next`) lands on the fallback route on the next hop - backends should return relative redirects or keep the prefix (same as nginx `proxy_pass` with trailing slash; see #155).
