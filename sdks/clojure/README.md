# etunnel for Clojure

[![Clojars](https://img.shields.io/badge/Clojars-dev.ggcode%2Fetunnel-5881d8?logo=clojure)](https://clojars.org/dev.ggcode/etunnel)

JDK HttpClient/WebSocket + cheshire.

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.ggcode.uk/tunnel`).

## Install

```
;; deps.edn — served from Maven Central (Clojars forbids shadowing it)
;; add https://repo1.maven.org/maven2/ (default in most setups) if missing
{:mvn/version "1.1.0"} with :deps {dev.ggcode/etunnel ...}
```

Requires JDK 11+.

## CLI

```
clojure -M -m etunnel 3000
```

Prints `TUNNEL_URL: https://<subdomain>.t.ggcode.uk` — that URL now routes to
your local service. Ctrl-C closes the tunnel.

## Embed

```clojure
(def t (et/connect! "wss://t.ggcode.uk/tunnel" 3000))
(println (:url t))
```

## Environment variables

| Variable | Meaning | Default |
|---|---|---|
| `ETUNNEL_SERVER` | tunnel server WebSocket URL | `wss://t.ggcode.uk/tunnel` |
| `ETUNNEL_LOCAL` | local port or `host:port` (or 1st CLI arg) | `3000` |
| `ETUNNEL_TOKEN` | paid-plan API key (optional) | - |
| `ETUNNEL_SUBDOMAIN` | custom subdomain (paid plans) | random |

## License

MIT — see [LICENSE](../../LICENSE). Buffer-based protocol client: request and
response bodies are buffered per stream (fine for webhooks/dashboards/demos;
use the [Go SDK](../../sdk) for streaming and WebSocket pass-through).
