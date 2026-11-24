# etunnel for C

[![GitHub release](https://img.shields.io/github/v/release/topcheer/etunnel?label=GitHub%20release)](https://github.com/topcheer/etunnel/releases)

libwebsockets + libcurl + json-c + OpenSSL.

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

`etunnel.c` is a **single-file command-line client**, not a linkable library:
it has its own `main()` and everything but the login helpers is `static`.
Embed it by building the binary once and spawning it next to your service
(see [Embed](#embed)).

## Install

```sh
make && sudo make install
```

Requires libwebsockets (>= 4), libcurl, json-c and OpenSSL, found through
`pkg-config`:

```sh
# Debian / Ubuntu (the package list CI installs on ubuntu-22.04)
apt install libwebsockets-dev libcurl4-openssl-dev libjson-c-dev
# macOS
brew install libwebsockets json-c
```

`make` builds `./etunnel` in this directory; `sudo make install` copies it to
`/usr/local/bin/etunnel` (`PREFIX=` to change). `make` was verified with
libwebsockets 5.1.0, libcurl 8.7.1, json-c 0.19 and OpenSSL 4.1.0 on macOS and
by CI's `make pack-c` on Ubuntu 22.04; `sudo make install` and the `apt` line
were not executed on the machine that verified this document.

> **Known issue in `etunnel` <= 1.7.35:** the CLI sends an `Origin` header
> (`cc.origin = host` in `main()`), which `etunneld` v1.7.29 and later reject
> with HTTP 403 - the client prints
> `etunnel: connection error: HS: ws upgrade response not 101` and (by
> default) keeps retrying. The fix is to not send `Origin` (`cc.origin = NULL`);
> apply that one-line change before `make` until a release carries it. Every
> run below was verified with that change applied.

## AI agent quick-start (copy this prompt)

```text
Integrate etunnel (C SDK) into this project to expose the local service on port <PORT> at a public HTTPS URL.

- Package: none to install - single-file CLI sdks/c/etunnel.c (libwebsockets, libcurl, json-c, OpenSSL via pkg-config) · Install: cd sdks/c && make && sudo make install
- Server: wss://t.etun.uk/tunnel (override with ETUNNEL_SERVER). Anonymous tier = random subdomain, 4h TTL; custom subdomain / longer TTL need a paid token (ETUNNEL_TOKEN; a free key holds one static subdomain).
- Minimal embed (verified against the SDK source) - the C SDK is a CLI, not a library, so spawn it and read the URL line:
  etunnel <PORT> > /tmp/etunnel.log 2>&1 &            # or: ./etunnel <PORT> after make
  until grep -q '^TUNNEL_URL:' /tmp/etunnel.log; do sleep 0.2; done
  TUNNEL_URL=$(sed -n 's/^TUNNEL_URL: //p' /tmp/etunnel.log)
- Rules: the child prints `TUNNEL_URL: <url>` on stdout (flushed) and keeps the tunnel open; keep it alive while the URL is needed and kill it to release the subdomain. This SDK is buffer-based (no WebSocket upgrades, no streaming) - fine for webhooks/APIs/dashboards; use the Go SDK for streaming/WebSocket.
- Do not invent other options. Full API/options: https://github.com/topcheer/etunnel/blob/main/sdks/c/README.md#environment-variables. Wire protocol: https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md
```

## CLI

```sh
etunnel 3000
```

Prints `TUNNEL_URL: https://<subdomain>.t.etun.uk` - that URL now routes to
your local service. Ctrl-C closes the tunnel.

**Reconnects automatically.** If the connection drops (including a server
restart) the CLI re-dials with exponential backoff (500ms -> 30s, +-20%
jitter, reset on success) and prints `TUNNEL_URL:` again. The subdomain is
**reclaimed**, not re-randomised: the client replays the server's signed
`resumeToken` in `hello.resume`, so links already shared keep working (the
server needs a stable `SESSION_SECRET`; see PROTOCOL.md section 1). Set
`ETUNNEL_RECONNECT=0` to restore the old one-shot behaviour.

## Embed

There is no `et_connect()` library entry point: the only non-`static`
functions are the CLI-login helpers `et_login()` / `et_login_free()`. The
supported embedding is a child process whose `TUNNEL_URL:` line you parse
(stdout is flushed right after the line is written):

```sh
./etunnel 3000 > /tmp/etunnel.log 2>&1 &
until grep -q '^TUNNEL_URL:' /tmp/etunnel.log; do sleep 0.2; done
TUNNEL_URL=$(sed -n 's/^TUNNEL_URL: //p' /tmp/etunnel.log)
echo "$TUNNEL_URL"      # https://<subdomain>.t.etun.uk
# ... when done: kill %1 - the tunnel closes with the process
```

## Environment variables

| Variable | Meaning | Default |
|---|---|---|
| `ETUNNEL_SERVER` | tunnel server WebSocket URL (`ws://` or `wss://`) | `wss://t.etun.uk/tunnel` |
| `ETUNNEL_LOCAL` | local port or loopback `host:port` (`127.0.0.1:3000`, `localhost:3000`), or 1st CLI arg | `3000` |
| `ETUNNEL_TOKEN` | API key (optional; a free key gets 1 static subdomain) | - |
| `ETUNNEL_SUBDOMAIN` | requested static subdomain (needs a key that holds it) | random |
| `ETUNNEL_RECONNECT` | `0` disables auto-reconnect (one-shot CLI) | enabled |
| `ETUNNEL_ROUTES` | multi-target route table, see below | - |

A non-loopback host in `ETUNNEL_LOCAL` is rejected up front (`exit 2`): this
SDK always dials `127.0.0.1`.

## License

MIT - see [LICENSE](../../LICENSE). Buffer-based protocol client: request and
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

```sh
# ETUNNEL_ROUTES="PREFIX=ADDR[,strip];..." (same shape as the Go CLI -route flag)
ETUNNEL_ROUTES="/api=127.0.0.1:8080,strip;/=127.0.0.1:3000" ./etunnel
# with routes set, argv[1]/ETUNNEL_LOCAL may be omitted
```

Semantics (identical in every SDK, pinned by tests): longest prefix
wins; matching is on segment boundaries (`/api` never matches `/apiv2`);
`strip` removes only the path part - the query string is carried over
byte-for-byte, an exact hit bottoms out at `/`; no matching route (only
possible without a `/` fallback) answers `404` at the SDK edge.

> Redirect note: with `strip`, a backend 302 to an absolute path (`/users/next`) lands on the fallback route on the next hop - backends should return relative redirects or keep the prefix (same as nginx `proxy_pass` with trailing slash; see #155).
