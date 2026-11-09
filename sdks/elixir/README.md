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
  [{:etunnel, "~> 1.0"}]
end
```

Requires Elixir >= 1.12 / OTP 24+.

## CLI

```
mix run --no-halt runner.exs 3000
```

Prints `TUNNEL_URL: https://<subdomain>.t.etun.uk` — that URL now routes to
your local service. Ctrl-C closes the tunnel.

## Embed

```elixir
{:ok, t} = Etunnel.start_link(server: "wss://t.etun.uk/tunnel", local: 3000)
Etunnel.url(t)
```

## Environment variables

| Variable | Meaning | Default |
|---|---|---|
| `ETUNNEL_SERVER` | tunnel server WebSocket URL | `wss://t.etun.uk/tunnel` |
| `ETUNNEL_LOCAL` | local port or `host:port` (or 1st CLI arg) | `3000` |
| `ETUNNEL_TOKEN` | paid-plan API key (optional) | - |
| `ETUNNEL_SUBDOMAIN` | custom subdomain (paid plans) | random |

## License

MIT — see [LICENSE](../../LICENSE). Buffer-based protocol client: request and
response bodies are buffered per stream (fine for webhooks/dashboards/demos;
use the [Go SDK](../../sdk) for streaming and WebSocket pass-through).
