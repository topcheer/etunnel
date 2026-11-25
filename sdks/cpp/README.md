# etunnel for C++

[![GitHub release](https://img.shields.io/github/v/release/topcheer/etunnel?label=GitHub%20release)](https://github.com/topcheer/etunnel/releases)

websocketpp + asio + libcurl + OpenSSL.

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

> **wss-only**: this SDK uses the TLS WebSocket transport; `ws://` URLs are
> rejected up front with an actionable error (#152). For local plaintext
> testing use another SDK or put TLS in front of the server (a self-signed
> certificate works together with `ETUNNEL_CA_FILE`).

`etunnel.cpp` is a **single-file command-line client**, not a linkable library:
it has its own `main()` and every other function is `static`. Embed it by
building the binary once and spawning it next to your service (see
[Embed](#embed)).

## Install

```sh
cmake -B build && cmake --build build
```

Builds `./build/etunnel` (C++17). Requires websocketpp, libcurl, OpenSSL and
one of:

- Boost.Asio from Boost **>= 1.66 and < 1.87** (websocketpp 0.8.x still uses
  `asio::io_service`, removed in 1.87), or
- standalone asio **<= 1.13** (newer releases no longer compile against
  websocketpp 0.8.x).

OpenSSL 3.x is required for the standalone-asio path (asio 1.13 reads
`ASN1_STRING` fields that OpenSSL 4 made opaque); since 1.7.36 CMake picks
Homebrew's `openssl@3` keg automatically when `OPENSSL_ROOT_DIR` is not set.

```sh
# Debian / Ubuntu 22.04 (CI installs the first three; Boost there is 1.74)
apt install libwebsocketpp-dev libasio-dev libboost-dev libcurl4-openssl-dev libssl-dev
# macOS (Homebrew): websocketpp + an OpenSSL 3 keg
brew install websocketpp openssl@3
```

The plain command above is what CI runs on Ubuntu 22.04 (`make pack-cpp`).
On macOS with Homebrew's Boost 1.92 / asio 1.38 the configure step stops with
`standalone asio ... is 1.38.2, too new for websocketpp 0.8.2 (needs <= 1.13.3)`;
point CMake at an asio 1.13 tree instead (`ASIO_DIR` is accepted as an alias).
Verified on the v1.7.36 tag source: websocketpp 0.8.2, asio 1.13.0,
OpenSSL 3.5.9, libcurl 8.7.1.

```sh
curl -sSL https://github.com/chriskohlhoff/asio/archive/refs/tags/asio-1-13-0.tar.gz | tar xz -C /tmp
cmake -B build -DASIO_INCLUDE_DIR=/tmp/asio-asio-1-13-0/asio/include && cmake --build build
```

## AI agent quick-start (copy this prompt)

```text
Integrate etunnel (C++ SDK) into this project to expose the local service on port <PORT> at a public HTTPS URL.

- Package: none to install - single-file CLI sdks/cpp/etunnel.cpp (websocketpp + asio or Boost.Asio < 1.87, libcurl, OpenSSL; built with CMake) · Install: cd sdks/cpp && cmake -B build && cmake --build build
- Server: wss://t.etun.uk/tunnel (override with ETUNNEL_SERVER; wss:// only - ws:// is rejected). Anonymous tier = random subdomain, 4h TTL; custom subdomain / longer TTL need a paid token (ETUNNEL_TOKEN; a free key holds one static subdomain).
- Minimal embed (verified against the SDK source) - the C++ SDK is a CLI, not a library, so spawn it and read the URL line:
  ./build/etunnel <PORT> > /tmp/etunnel.log 2>&1 &
  until LC_ALL=C grep -a -q '^TUNNEL_URL:' /tmp/etunnel.log; do sleep 0.2; done
  TUNNEL_URL=$(LC_ALL=C sed -n 's/^TUNNEL_URL: //p' /tmp/etunnel.log)
- Rules: the child prints `TUNNEL_URL: <url>` on stdout (plus websocketpp trace lines) and keeps the tunnel open; keep it alive while the URL is needed and kill it to release the subdomain. TLS chain + hostname verification is on by default; for a private CA set ETUNNEL_CA_FILE. This SDK is buffer-based (no WebSocket upgrades, no streaming) - fine for webhooks/APIs/dashboards; use the Go SDK for streaming/WebSocket.
- Do not invent other options. Full API/options: https://github.com/topcheer/etunnel/blob/main/sdks/cpp/README.md#environment-variables. Wire protocol: https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md
```

## CLI

```sh
./build/etunnel 3000
```

Prints `TUNNEL_URL: https://<subdomain>.t.etun.uk` - that URL now routes to
your local service. Ctrl-C closes the tunnel. websocketpp's connection trace
(`[connect] ...`, `[frame_header] ...`) is written to the same stdout; the
`TUNNEL_URL:` line is the one to parse.

**Reconnects automatically.** If the connection drops (including a server
restart) the CLI re-dials with exponential backoff (500ms -> 30s, +-20%
jitter, reset on success) and prints `TUNNEL_URL:` again. The subdomain is
**reclaimed**, not re-randomised: the client replays the server's signed
`resumeToken` in `hello.resume`, so links already shared keep working (the
server needs a stable `SESSION_SECRET`; see PROTOCOL.md section 1). Set
`ETUNNEL_RECONNECT=0` to restore the old one-shot behaviour.

**TLS is verified by default** - chain against the system trust store plus a
hostname pin; a self-signed or mis-issued certificate fails the handshake
(`TLS handshake failed`). `ETUNNEL_CA_FILE` adds a trust root for a private
CA; `ETUNNEL_INSECURE_TLS=1` disables both checks with a loud warning.

## Embed

There is no non-`static` library entry point in `etunnel.cpp`. The supported
embedding is a child process whose `TUNNEL_URL:` line you parse:

```sh
./build/etunnel 3000 > /tmp/etunnel.log 2>&1 &
until LC_ALL=C grep -a -q '^TUNNEL_URL:' /tmp/etunnel.log; do sleep 0.2; done
TUNNEL_URL=$(LC_ALL=C sed -n 's/^TUNNEL_URL: //p' /tmp/etunnel.log)
echo "$TUNNEL_URL"      # https://<subdomain>.t.etun.uk
# ... when done: kill %1 - the tunnel closes with the process
```

## Handshake

The `hello` frame carries the SDK version: both `"version"` and the `client`
string come from `ETUNNEL_VERSION` in `etunnel.cpp`, which
`scripts/bump-release.py` stamps from `VERSION` (`-DETUNNEL_VERSION=...`
overrides it at build time). Shape when no token/subdomain is set:

```json
{"type":"hello","proto":1,"version":"1.7.39","client":"etunnel-cpp/1.7.39"}
```

Servers that predate the field ignore it. A server whose SDK floor is newer
answers an `error` frame with code `client_outdated`; the CLI then prints the
server's message verbatim on stderr - it names the required version and the
upgrade URL (`https://github.com/topcheer/etunnel`) - and exits with status 2
without retrying. `proto_version` is handled the same way. Any other `error`
code is printed as `etunnel: server error: <code>: <message>` and the
connection ends (the reconnect loop then applies, as before).
`tests/test_client_version.sh` pins the version field and the
`client_outdated` exit.

## Environment variables

| Variable | Meaning | Default |
|---|---|---|
| `ETUNNEL_SERVER` | tunnel server WebSocket URL (`wss://` only) | `wss://t.etun.uk/tunnel` |
| `ETUNNEL_LOCAL` | local **port number** (or 1st CLI arg); `host:port` is not parsed here - `127.0.0.1:3000` would be read as port 127 | `3000` |
| `ETUNNEL_TOKEN` | API key (optional; a free key gets 1 static subdomain) | - |
| `ETUNNEL_SUBDOMAIN` | requested static subdomain (needs a key that holds it) | random |
| `ETUNNEL_RECONNECT` | `0` disables auto-reconnect (one-shot CLI) | enabled |
| `ETUNNEL_ROUTES` | multi-target route table, see below | - |
| `ETUNNEL_CA_FILE` | extra PEM trust root for a private CA | - |
| `ETUNNEL_INSECURE_TLS` | `1` disables chain **and** hostname verification (MITM exposure; prints a warning) | - |

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
ETUNNEL_ROUTES="/api=127.0.0.1:8080,strip;/=127.0.0.1:3000" ./build/etunnel
# with routes set, the positional local port may be omitted
```

Semantics (identical in every SDK, pinned by tests): longest prefix
wins; matching is on segment boundaries (`/api` never matches `/apiv2`);
`strip` removes only the path part - the query string is carried over
byte-for-byte, an exact hit bottoms out at `/`; no matching route (only
possible without a `/` fallback) answers `404` at the SDK edge.

> Redirect note: with `strip`, a backend 302 to an absolute path (`/users/next`) lands on the fallback route on the next hop - backends should return relative redirects or keep the prefix (same as nginx `proxy_pass` with trailing slash; see #155).
