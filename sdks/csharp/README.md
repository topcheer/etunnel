# etunnel for C# / .NET

[![NuGet](https://img.shields.io/nuget/v/etunnel?label=NuGet%20etunnel)](https://www.nuget.org/packages/etunnel)
[![.NET](https://img.shields.io/badge/.NET-10%2B-512bd4)](https://www.nuget.org/packages/etunnel)

System.Net.WebSockets + HttpClient, zero NuGet dependencies. Published on
nuget.org as `etunnel` (verified: 1.7.36).

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

## Install

NuGet package (.NET 10+; verified with `dotnet` 11.0.100):

```sh
dotnet add package etunnel
```

The 1.7.36 package ships `lib/net10.0` **only** - a `net8.0`/`net9.0`
project fails to restore with `NU1202: Package etunnel 1.7.36 is not
compatible with net8.0` (verified). For older targets, vendor the single
file instead:

```sh
curl -O https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/csharp/Etunnel.cs
```

`Etunnel.cs` has no namespace and no package references; add it to any
SDK-style project (it still needs a .NET runtime with `System.Net.WebSockets`).

## AI agent quick-start (copy this prompt)

```text
Integrate etunnel (C# SDK) into this project to expose the local service on port 3000 at a public HTTPS URL.

- Package: etunnel on NuGet (net10.0 only) · Install: dotnet add package etunnel (resolved 1.7.36; for net8.0/net9.0 vendor sdks/csharp/Etunnel.cs instead). Standalone CLI: dotnet run --file Etunnel.cs 3000 (.NET 10 SDK)
- Server: wss://t.etun.uk/tunnel (override with ETUNNEL_SERVER). Anonymous tier = random subdomain, 4h TTL; custom subdomain / longer TTL need a paid token (token: "..." or ETUNNEL_TOKEN).
- Minimal embed (verified against the SDK source and run against a live server):
var server = Environment.GetEnvironmentVariable("ETUNNEL_SERVER") ?? "wss://t.etun.uk/tunnel";
var tunnel = await Etunnel.Connect(server, "3000");
Console.WriteLine("TUNNEL_URL: " + tunnel.Url);

Console.CancelKeyPress += async (_, e) => { e.Cancel = true; await tunnel.Close(); };
await Task.Delay(Timeout.Infinite);   // keep the tunnel open
- Rules: the Etunnel class lives in the global namespace (no `using Etunnel;`). Print the public URL as `TUNNEL_URL: <url>` and keep the process alive; await tunnel.Close() on shutdown to release the subdomain. This SDK is buffer-based (no WebSocket upgrades, no streaming) - fine for webhooks/APIs/dashboards. Named arguments: Etunnel.Connect(server, "3000", token: "...", subdomain: "myapp", routes: new[] { new Etunnel.Route { Prefix = "/api", Addr = "127.0.0.1:8080", Strip = true } }).
- Do not invent other options. Full API/options: https://github.com/topcheer/etunnel/blob/main/sdks/csharp/README.md#api and #multi-target-routing. Wire protocol: https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md
```

## CLI

```sh
dotnet run --file Etunnel.cs 3000
```

Run from `sdks/csharp` (a .NET 10+ SDK; file-based app). `--file` is the
unambiguous form: inside `sdks/csharp` a plain `dotnet run Etunnel.cs 3000`
is picked up by the sibling `Etunnel.csproj` and passes `Etunnel.cs` as the
*local port* argument, which yields a `502` tunnel - do not use it there.
On a machine whose only runtime is newer than the one the file targets
(verified: .NET 11 runtime, file targets net10.0) prefix the command with
`DOTNET_ROLL_FORWARD=Major`.

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

```csharp
var server = Environment.GetEnvironmentVariable("ETUNNEL_SERVER") ?? "wss://t.etun.uk/tunnel";
var tunnel = await Etunnel.Connect(server, "3000");
Console.WriteLine("TUNNEL_URL: " + tunnel.Url);

Console.CancelKeyPress += async (_, e) => { e.Cancel = true; await tunnel.Close(); };
await Task.Delay(Timeout.Infinite);   // keep the tunnel open
```

Top-level statements in a console project that references the NuGet package
(verified: `dotnet new console` + `dotnet add package etunnel --version 1.7.36` → 1.7.36,
`dotnet run` printed the URL and the tunnel served the local service). No
`using Etunnel;` - the class is in the global namespace. The vendored route
was verified too: `Etunnel.cs` + the snippet above in a `net8.0` console
project builds with 0 errors (one harmless `CS7022` warning: the file's
`Cli.Main` entry point is ignored in favour of your top-level statements)
and the tunnel served traffic.

## API

| Symbol | Returns / effect |
|---|---|
| `await Etunnel.Connect(server, local, token: null, subdomain: null, resume: null, routes: null)` | `Task<Etunnel.Tunnel>` (completes after `welcome`; `local` is a port or `"host:port"` string, may be `null` when `routes` is given) |
| `Tunnel.Url` / `.ResumeToken` | properties from the `welcome` frame (pass `ResumeToken` back as `resume` to reclaim the subdomain) |
| `await Tunnel.Close()` | sends `bye` + a WebSocket close, releasing the subdomain |
| `Etunnel.Route { Prefix, Addr, Strip }` | one route row for `routes` |
| `Etunnel.NormalizeRoutes(Route[])` / `Etunnel.RouteFor(Route[], pathAndQuery)` | the route-table helpers the tunnel itself uses |

Options: named arguments - `Etunnel.Connect(server, "3000", token: "et_...", subdomain: "myapp")`.

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

```csharp
var t = await Etunnel.Connect("wss://t.example.com/tunnel", null, routes: new[] {
    new Etunnel.Route { Prefix = "/api", Addr = "127.0.0.1:8080", Strip = true },
    new Etunnel.Route { Prefix = "/", Addr = "127.0.0.1:3000" },
});
```

Semantics (identical in every SDK, pinned by tests): longest prefix
wins; matching is on segment boundaries (`/api` never matches `/apiv2`);
`strip` removes only the path part - the query string is carried over
byte-for-byte, an exact hit bottoms out at `/`; no matching route (only
possible without a `/` fallback) answers `404` at the SDK edge.
Verified against a live server: `/api/items?id=7` reached the local service
as `/items?id=7`, `/other` took the `/` route unchanged.

> Redirect note: with `strip`, a backend 302 to an absolute path (`/users/next`) lands on the fallback route on the next hop - backends should return relative redirects or keep the prefix (same as nginx `proxy_pass` with trailing slash; see #155).

> Throughput note (#243 M9): the response header block, DATA and FIN for a
> stream are sent while holding the tunnel's send gate, so one very large
> response can delay other streams on the same tunnel (head-of-line). This
> is a deliberate trade of the single-lock design (#54); the Go SDK and the
> other per-stream-dispatch SDKs do not have this limitation. If you tunnel
> large payloads alongside latency-sensitive traffic, prefer those SDKs.
