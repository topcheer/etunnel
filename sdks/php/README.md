# etunnel for PHP

[![Packagist](https://img.shields.io/packagist/v/ggcode/etunnel)](https://packagist.org/packages/ggcode/etunnel)

Streams + ext-openssl only — no curl/guzzle needed.

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

## Install

```
composer require ggcode/etunnel
```

Requires PHP >= 8.0 with ext-openssl. The Packagist package
`ggcode/etunnel` is the whole repository as a library: its autoloader pulls in
`sdks/php/etunnel.php` (installed at `vendor/ggcode/etunnel/sdks/php/`).
Single file, no Composer: `curl -O https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/php/etunnel.php`.

## AI agent quick-start (copy this prompt)

````text
Integrate etunnel (PHP SDK) into this project to expose the local service on port <PORT> at a public HTTPS URL.

- Package: `ggcode/etunnel` on Packagist · Install: `composer require ggcode/etunnel` (PHP >= 8.0, ext-openssl)
- Server: wss://t.etun.uk/tunnel (override with ETUNNEL_SERVER). Anonymous tier = random subdomain, 4h TTL; custom subdomain / longer TTL need a paid token (opts.token or ETUNNEL_TOKEN).
- Minimal embed (verified against the SDK source):
```php
<?php
require __DIR__ . '/vendor/autoload.php';
Etunnel::connect('wss://t.etun.uk/tunnel', 3000, null, null, function (string $url): void {
    echo "TUNNEL_URL: {$url}\n";
});
```
- Rules: print the public URL as `TUNNEL_URL: <url>` inside the onUrl callback (connect() blocks and never returns while the tunnel is up) and keep the process alive; there is no run() method. This SDK is buffer-based (no WebSocket upgrades, no streaming, sequential requests) - fine for webhooks/APIs/dashboards.
- Do not invent other options. Full API/options: #embed and #multi-target-routing. Wire protocol: https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md
````

## CLI

```
php etunnel.php 3000                                    # single file
php vendor/ggcode/etunnel/sdks/php/etunnel.php 3000     # after composer require
```

After `composer require`, `php vendor/bin/etunnel.php 3000` works too
(Composer's bin proxy is recognised since 1.7.37; in <= 1.7.36 it exited
silently - call the file under `vendor/ggcode/etunnel/sdks/php/` instead).

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

```php
<?php
require __DIR__ . '/vendor/autoload.php';

// connect() blocks and serves requests itself; the callback fires once on `welcome`.
Etunnel::connect('wss://t.etun.uk/tunnel', 3000, null, null, function (string $url): void {
    echo "TUNNEL_URL: {$url}\n";
});
```

`Etunnel::connect(string $server, $local, ?string $token = null, ?string $subdomain = null,
?callable $onUrl = null, ?string $resume = null, ?array $routes = null)` is
**blocking**: it sends `hello`, waits for `welcome`, calls `$onUrl($url)`, then
serves requests until the server closes the tunnel - only then does it return
the `Etunnel` object (`$tunnel->url`, `$tunnel->close()`). There is no `run()`
method and no thread: requests are served sequentially. `Etunnel::$lastResumeToken`
holds the server-signed token of the last `welcome`; replay it via `$resume` to
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

```php
// local is optional when routes are given (null since 1.7.37; a placeholder
// port string also works - the route table always wins)
Etunnel::connect("wss://t.example.com/tunnel", null, routes: [
    ["prefix" => "/api", "addr" => "127.0.0.1:8080", "strip" => true],
    ["prefix" => "/",    "addr" => "127.0.0.1:3000"],
], onUrl: fn(string $url) => print("TUNNEL_URL: $url\n"));
```

Semantics (identical in every SDK, pinned by tests): longest prefix
wins; matching is on segment boundaries (`/api` never matches `/apiv2`);
`strip` removes only the path part - the query string is carried over
byte-for-byte, an exact hit bottoms out at `/`; no matching route (only
possible without a `/` fallback) answers `404` at the SDK edge.

> Redirect note: with `strip`, a backend 302 to an absolute path (`/users/next`) lands on the fallback route on the next hop - backends should return relative redirects or keep the prefix (same as nginx `proxy_pass` with trailing slash; see #155).
