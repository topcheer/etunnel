# etunnel for Python

[![PyPI](https://img.shields.io/pypi/v/etunnel?label=PyPI%20etunnel)](https://pypi.org/project/etunnel/)

`connect()` returns as soon as the server assigns a URL; the tunnel then serves
requests on background threads. Only dependency: `websocket-client`.

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

## Install

```
pip install etunnel
```

Python >= 3.8 (package metadata; verified on 3.15), `websocket-client` installed
automatically. Use version **>= 1.7.36**: 1.7.29-1.7.35 on PyPI are rejected by
current servers (see [Known issues](#known-issues)).

## AI agent quick-start (copy this prompt)

```text
Integrate etunnel (Python SDK) into this project to expose the local service on port <PORT> at a public HTTPS URL.

- Package: `etunnel` on PyPI, version >= 1.7.36 (1.7.29-1.7.35 are rejected by current servers) · Install: `pip install etunnel`
- Server: wss://t.etun.uk/tunnel (override with ETUNNEL_SERVER). Anonymous tier = random subdomain, 4h TTL; custom subdomain / longer TTL need a paid token (opts.token or ETUNNEL_TOKEN).
- Minimal embed (verified against the SDK source):
from etunnel import connect

tunnel = connect("wss://t.etun.uk/tunnel", <PORT>)
print("TUNNEL_URL:", tunnel["url"])

try:
    input("press Enter to stop\n")
finally:
    tunnel["close"]()
- Rules: print the public URL as `TUNNEL_URL: <url>` and keep the process alive (the worker threads are daemons - the process must not return); call `tunnel["close"]()` on shutdown to release the subdomain. This SDK is buffer-based (no WebSocket upgrades, no streaming) - fine for webhooks/APIs/dashboards.
- Do not invent other options. Full API/options: the `API` and `Multi-target routing` sections of this README (`sdks/python/README.md#api`). Wire protocol: https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md
```

## CLI

```
etunnel 3000
python -m etunnel 3000
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

```python
from etunnel import connect

tunnel = connect("wss://t.etun.uk/tunnel", 3000)
print("TUNNEL_URL:", tunnel["url"])

try:
    input("press Enter to stop\n")   # keep the process alive: the worker threads are daemons
finally:
    tunnel["close"]()
```

Inside a server process (gunicorn/uvicorn) skip the `input()` loop and simply
keep the process alive; `connect()` never blocks.

## API

| Symbol | Returns / effect |
|---|---|
| `connect(server, local, opts=None)` | `dict` with `url`, `hostname`, `subdomain`, `resumeToken`, `plan`, `close` — all taken from the `welcome` frame |
| `tunnel["close"](reason="client closed")` | sends `bye`, closes the socket, frees the subdomain |
| `login(server, opts=None)` | `dict` `{api_token, subdomains, plan, expires_at}` — browser login; not exercised in this audit |

`local` is a required positional argument: a port number or `"host:port"`. Pass
`None` together with `opts["routes"]`.

`opts`:

| Option | Type | Meaning |
|---|---|---|
| `token` | `str` | paid-plan API key: custom subdomain + longer TTL (CLI: `ETUNNEL_TOKEN`) |
| `subdomain` | `str` | request this subdomain instead of a random one (paid plans; CLI: `ETUNNEL_SUBDOMAIN`) |
| `client` | `str` | client identifier sent in `hello` (default `etunnel-python/1.0`) |
| `resume` | `str` | `resumeToken` of an earlier tunnel: reclaim the same subdomain |
| `routes` | `list[dict]` | multi-target routing, see below; each dict has `prefix`, `addr` and optional `strip`. Pass `None` as `local` |
| `on_close` | `callable(reason)` | called when the tunnel ends: your `close()`, a server `bye`, a ping timeout or a dropped socket (see [Known issues](#known-issues)) |

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

One public hostname, several local services - the SPA prod-bundle shape
(`/` to the built frontend, `/api` to the backend) without CORS or a
second tunnel. Routing is client-side; the protocol is unchanged.

```python
tunnel = connect("wss://t.example.com/tunnel", None, {
    "routes": [
        {"prefix": "/api", "addr": "127.0.0.1:8080", "strip": True},
        {"prefix": "/", "addr": "127.0.0.1:3000"},
    ],
})
print("TUNNEL_URL:", tunnel["url"])
input()   # keep the process alive - the worker threads are daemons
```

Semantics (identical in every SDK, pinned by tests): longest prefix wins; matching is on segment boundaries (`/api` never matches `/apiv2`); `strip` removes only the path part - the query string is carried over byte-for-byte, an exact hit bottoms out at `/`; no matching route (only possible without a `/` fallback) answers `404` at the SDK edge.

> Redirect note: with `strip`, a backend 302 to an absolute path (`/users/next`) lands on the fallback route on the next hop - backends should return relative redirects or keep the prefix (same as nginx `proxy_pass` with trailing slash; see #155).

## Known issues

- **PyPI 1.7.29-1.7.35 cannot connect to etunneld >= 1.7.29.** `connect()` fails
  with `WebSocketBadStatusException: Handshake status 403 Forbidden ...
  browser-origin tunnels are not supported; use the SDK`: `websocket-client`
  sends an `Origin` header by default, and the server rejects every tunnel
  handshake that carries one. The fix (suppressing the header) needs a release
  **>= 1.7.36**.
- **`on_close` fires twice on an explicit `close()`** (seen on 1.7.35 against a
  real server): first with the server's `read closed` bye echo, then with your
  own reason. Do not reconnect from inside `on_close` without guarding against
  the second call.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `Handshake status 403 Forbidden ... browser-origin tunnels are not supported` | 1.7.29-1.7.35 send an `Origin` header; servers >= 1.7.29 refuse it | install >= 1.7.36 (see Known issues) |
| `TypeError: connect() missing 1 required positional argument` | `connect(server, opts={...})` - `opts` is not a keyword-only argument | `connect(server, None, {...})`, or `connect(server, 3000, {...})` |
| The tunnel is gone right after `connect()` returns | the worker threads are daemons and the main thread exited | keep the process alive (see the Embed section) |
| `on_close` runs twice on `close()` | server `bye` echo plus your own call (see Known issues) | guard the callback against a second run |
