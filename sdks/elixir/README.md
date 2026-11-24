# etunnel for Elixir

[![Hex](https://img.shields.io/hexpm/v/etunnel?label=Hex%20etunnel)](https://hex.pm/packages/etunnel)

websockex + jason; local requests via :httpc.

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

## Install

```
# mix.exs
defp deps do
  [{:etunnel, "~> 1.7.0"}]
end
```

Requires Elixir >= 1.12 / OTP 24+. Deps: `websockex` (~> 0.4) and `jason`
(~> 1.3). The Hex package contains only `lib/` + `mix.exs` (`runner.exs`, the
CLI, is **not** shipped).

> **Status 1.7.35:** `Etunnel.start_link/1` crashes on every connect with
> `{:error, %KeyError{key: :last_activity}}` - the GenServer state map in
> `lib/etunnel.ex` lacks `last_pong`/`last_activity`. The embed below was
> verified against the source with those two keys added; wait for the next
> release (1.7.36) or depend on a fixed git revision.

## AI agent quick-start (copy this prompt)

````text
Integrate etunnel (Elixir SDK) into this project to expose the local service on port <PORT> at a public HTTPS URL.

- Package: `etunnel` on hex.pm (Elixir >= 1.12; deps websockex + jason) · Install: `{:etunnel, "~> 1.7.0"}` in mix.exs deps, then `mix deps.get`
- Server: wss://t.etun.uk/tunnel (override with ETUNNEL_SERVER). Anonymous tier = random subdomain, 4h TTL; custom subdomain / longer TTL need a paid token (opts.token or ETUNNEL_TOKEN).
- Minimal embed (verified against the SDK source):
```elixir
{:ok, t} = Etunnel.start_link(server: "wss://t.etun.uk/tunnel", local: 3000)
IO.puts("TUNNEL_URL: " <> Etunnel.url(t))
Process.sleep(:infinity)
```
- Rules: print the public URL as `TUNNEL_URL: <url>` (Etunnel.url/1 blocks up to 10 s for welcome) and keep the process alive; call `Etunnel.close(pid)` on shutdown to release the subdomain. This SDK is buffer-based (no WebSocket upgrades, no streaming) - fine for webhooks/APIs/dashboards.
- Do not invent other options. Full API/options: #embed and #multi-target-routing. Wire protocol: https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md
- Known issue in 1.7.35: Etunnel.start_link/1 raises KeyError :last_activity; the snippet works once the GenServer state carries last_pong/last_activity (fix expected in 1.7.36).
````

## CLI

```
cd sdks/elixir && mix deps.get && mix run --no-halt runner.exs 3000   # source checkout only (runner.exs is not in the Hex package)
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

```elixir
{:ok, t} = Etunnel.start_link(server: "wss://t.etun.uk/tunnel", local: 3000)
url = Etunnel.url(t)              # waits ≤10s for welcome; {:error, reason} on failure
IO.puts("TUNNEL_URL: " <> url)

# the server's signed resume token arrives at the start_link caller:
tok =
  receive do
    {:etunnel_resume_token, tok} -> tok
  end

Etunnel.close(t)                  # sends bye and tears down the connection
{:ok, t2} = Etunnel.start_link(server: "wss://t.etun.uk/tunnel", local: 3000, resume: tok)
```

Options (`Etunnel.start_link/1`): `server:` (required), `local:` (port or
`"host:port"`; omit when `routes:` is given), `token:`, `subdomain:`, `resume:`,
`routes:` (see below), `tls_verify:`.

`Etunnel.url/1` waits up to 10s for the welcome frame and returns the URL, or
`{:error, reason}` — including the structured `{code, message}` of a server
error frame, which also stops the tunnel process. Reconnecting with the
`resumeToken` reclaims the same subdomain.

TLS verifies the peer against the OS CA store by default (`verify_peer` +
SNI). Pass `tls_verify: :verify_none` only for throwaway self-signed servers.

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

```elixir
{:ok, pid} = Etunnel.start_link(
  server: "wss://t.example.com/tunnel",
  routes: [
    %{prefix: "/api", addr: "127.0.0.1:8080", strip: true},
    %{prefix: "/", addr: "127.0.0.1:3000"}
  ])
```

Semantics (identical in every SDK, pinned by tests): longest prefix
wins; matching is on segment boundaries (`/api` never matches `/apiv2`);
`strip` removes only the path part - the query string is carried over
byte-for-byte, an exact hit bottoms out at `/`; no matching route (only
possible without a `/` fallback) answers `404` at the SDK edge.

> Redirect note: with `strip`, a backend 302 to an absolute path (`/users/next`) lands on the fallback route on the next hop - backends should return relative redirects or keep the prefix (same as nginx `proxy_pass` with trailing slash; see #155).
