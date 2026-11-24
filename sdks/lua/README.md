# etunnel for Lua

[![LuaRocks](https://img.shields.io/badge/LuaRocks-ggcode.etunnel-8b4bb8?logo=lua)](https://luarocks.org/modules/topcheer/ggcode.etunnel)

luasocket + luasec (hand-rolled WebSocket with client SNI; lua-http is used
for `wss` when that rock is installed).

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

## Install

```
# needs the LuaSocket and LuaSec rocks (Lua 5.3+)
# single file:
curl -O https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/lua/etunnel.lua
# or build the rock from a source checkout of the repo:
cd sdks/lua && luarocks make                 # -> <tree>/share/lua/<ver>/ggcode/etunnel.lua
```

Requires Lua 5.3+ (`string.pack`). **`luarocks install ggcode.etunnel`
currently fails** (1.7.35-1: `Failed copying etunnel.lua` - the published
src.rock points at a git tag where the file lives under `sdks/lua/`); use
the single file or `luarocks make` until the rock is republished.

## AI agent quick-start (copy this prompt)

````text
Integrate etunnel (Lua SDK) into this project to expose the local service on port <PORT> at a public HTTPS URL.

- Package: `ggcode.etunnel` on LuaRocks (but `luarocks install` is broken in 1.7.35-1) · Install: `curl -O https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/lua/etunnel.lua`; needs the LuaSocket + LuaSec rocks (Lua 5.3+)
- Server: wss://t.etun.uk/tunnel (override with ETUNNEL_SERVER). Anonymous tier = random subdomain, 4h TTL; custom subdomain / longer TTL need a paid token (opts.token or ETUNNEL_TOKEN).
- Minimal embed (verified against the SDK source; append to a copy of etunnel.lua named myapp.lua, run `lua myapp.lua`):
```lua
local t = connect('wss://t.etun.uk/tunnel', 3000, nil, nil, nil, nil)
print('TUNNEL_URL: ' .. tostring(t.url))
io.stdout:flush()
t.run()
```
- Rules: print the public URL as `TUNNEL_URL: <url>` and keep the process alive (t.run() blocks); call t.close() on shutdown to release the subdomain. This SDK is buffer-based (no WebSocket upgrades, no streaming, sequential requests) - fine for webhooks/APIs/dashboards.
- Do not invent other options. Full API/options: #embed and #multi-target-routing. Wire protocol: https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md
````

## CLI

```
lua etunnel.lua 3000
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

`etunnel.lua` exports no module table: `connect` is file-local and the CLI
block only runs when `etunnel.lua` itself is executed. Copy the file next to
your code, rename it (e.g. `myapp.lua`) and append your main; run
`lua myapp.lua`.

```lua
local t = connect('wss://t.etun.uk/tunnel', 3000, nil, nil, nil, nil)
print('TUNNEL_URL: ' .. tostring(t.url))
io.stdout:flush()
t.run()   -- blocking serve loop (one request at a time); returns when the tunnel drops
-- on shutdown: t.close()   (sends `bye`, frees the subdomain)
```

`connect(server, local_port, token, subdomain, resume, routes)` blocks until
`welcome` and returns `{url, resumeToken, run(), close()}`; `run()` must be
called to serve requests (sequential). `local_port` is a port or
`"127.0.0.1:port"`; replay `t.resumeToken` via the `resume` argument to
reclaim the same subdomain.

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

```lua
local t = connect("wss://t.example.com/tunnel", nil, nil, nil, nil, {
  { prefix = "/api", addr = "127.0.0.1:8080", strip = true },
  { prefix = "/", addr = "127.0.0.1:3000" },
})
```

Semantics (identical in every SDK, pinned by tests): longest prefix
wins; matching is on segment boundaries (`/api` never matches `/apiv2`);
`strip` removes only the path part - the query string is carried over
byte-for-byte, an exact hit bottoms out at `/`; no matching route (only
possible without a `/` fallback) answers `404` at the SDK edge.

> Redirect note: with `strip`, a backend 302 to an absolute path (`/users/next`) lands on the fallback route on the next hop - backends should return relative redirects or keep the prefix (same as nginx `proxy_pass` with trailing slash; see #155).

## Environment

Runs on Lua 5.3-5.5 (verified on 5.5.1 with LuaSocket 3.1 + LuaSec 1.3,
without lua-http). `luac -p` (5.4) is the CI gate.
