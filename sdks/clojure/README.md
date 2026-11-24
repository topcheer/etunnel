# etunnel for Clojure

[![Clojars](https://img.shields.io/badge/Clojars-dev.ggcode%2Fetunnel--clj-5881d8?logo=clojure)](https://clojars.org/dev.ggcode/etunnel-clj)
[![JDK](https://img.shields.io/badge/JDK-11%2B-orange)](https://clojars.org/dev.ggcode/etunnel-clj)

JDK HttpClient/WebSocket + cheshire. Published on Clojars as
`dev.ggcode/etunnel-clj` (verified: 1.7.35).

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

## Install

```clojure
;; deps.edn
{:deps {dev.ggcode/etunnel-clj {:mvn/version "1.7.35"}
        cheshire/cheshire       {:mvn/version "5.13.0"}}}
```

**`cheshire` must be listed explicitly.** The published pom declares no
dependencies, so a deps.edn with only `etunnel-clj` fails at load time with
`Could not locate cheshire/core__init.class` (verified against the 1.7.35
artifact). `1.7.35` is the version this README was verified against.

Or copy the single file `etunnel.clj` into your project (namespace `etunnel`)
and add cheshire yourself.

Requires JDK 11+.

## AI agent quick-start (copy this prompt)

```text
Integrate etunnel (Clojure SDK) into this project to expose the local service on port 3000 at a public HTTPS URL.

- Package: dev.ggcode/etunnel-clj on Clojars (plus cheshire, which the pom does not pull in) · Install: add {:deps {dev.ggcode/etunnel-clj {:mvn/version "1.7.35"} cheshire/cheshire {:mvn/version "5.13.0"}}} to deps.edn (JDK 11+). Standalone CLI from the repo: clojure -M -m etunnel 3000
- Server: wss://t.etun.uk/tunnel (override with ETUNNEL_SERVER). Anonymous tier = random subdomain, 4h TTL; custom subdomain / longer TTL need a paid token (:token or ETUNNEL_TOKEN).
- Minimal embed (verified against the SDK source and run against a live server):
(require '[etunnel :as et])

(def t (et/connect! "wss://t.etun.uk/tunnel" 3000))
(println "TUNNEL_URL:" (:url t))
@(promise) ; keep the tunnel alive; it closes with the JVM
- Rules: print the public URL as `TUNNEL_URL: <url>` and keep the process alive; call ((:close! t)) on shutdown to release the subdomain (connect! returns a plain map with :url, :ws, :resume-token and :close!, not an atom). This SDK is buffer-based (no WebSocket upgrades, no streaming) - fine for webhooks/APIs/dashboards. Keyword arguments: (et/connect! server 3000 :token "..." :subdomain "myapp" :routes [{:prefix "/api" :addr "127.0.0.1:8080" :strip true}]).
- Do not invent other options. Full API/options: https://github.com/topcheer/etunnel/blob/main/sdks/clojure/README.md#api and #multi-target-routing. Wire protocol: https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md
```

## CLI

```sh
clojure -M -m etunnel 3000
```

Run from the `sdks/clojure` directory of a checkout (its `deps.edn` puts `.`
on the classpath and pulls cheshire). Verified with Clojure CLI 1.12.7 on
JDK 17.

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

```clojure
(require '[etunnel :as et])

(def t (et/connect! "wss://t.etun.uk/tunnel" 3000))
(println "TUNNEL_URL:" (:url t))

;; later: ((:close! t)) sends `bye` and releases the subdomain
@(promise) ; keep the tunnel alive
```

`connect!` blocks until `welcome`, so `(:url t)` is set when it returns.
Verified against a live server with `clojure -M -m app` and a deps.edn
listing `etunnel-clj` + cheshire.

## API

| Symbol | Returns / effect |
|---|---|
| `(connect! server local & {:keys [token subdomain resume routes]})` | state map `{:url :ws :resume-token :close!}` (blocking until `welcome`; `local` is a port or `"host:port"`, may be `nil` when `:routes` is given) |
| `(:url t)` | public HTTPS URL from the `welcome` frame |
| `(:resume-token t)` | server-signed token; pass it back as `:resume` to reclaim the subdomain |
| `((:close! t))` | sends `bye` + a WebSocket close, releasing the subdomain |

Options: keyword arguments - `(et/connect! server 3000 :subdomain "myapp" :token "et_...")`; `:routes` takes `{:prefix :addr :strip}` rows (see below).

> Known issue in 1.7.35: the SDK prints `DBG stored-ok` to stdout for every
> proxied request (a leftover debug `println` in the `req` handler). It is
> harmless but noisy; the header comment of `etunnel.clj` also shows an
> outdated `(:url @t)` / `(etunnel/close! @t)` form - the map is not an atom
> and there is no `close!` var, use `((:close! t))`.

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

```clojure
(def t (etunnel/connect! "wss://t.example.com/tunnel" nil
         :routes [{:prefix "/api" :addr "127.0.0.1:8080" :strip true}
                  {:prefix "/" :addr "127.0.0.1:3000"}]))
```

Semantics (identical in every SDK, pinned by tests): longest prefix
wins; matching is on segment boundaries (`/api` never matches `/apiv2`);
`strip` removes only the path part - the query string is carried over
byte-for-byte, an exact hit bottoms out at `/`; no matching route (only
possible without a `/` fallback) answers `404` at the SDK edge.
Verified against a live server: `/api/items?id=7` reached the local service
as `/items?id=7`, `/other` took the `/` route unchanged.

> Redirect note: with `strip`, a backend 302 to an absolute path (`/users/next`) lands on the fallback route on the next hop - backends should return relative redirects or keep the prefix (same as nginx `proxy_pass` with trailing slash; see #155).
