# etunnel for Perl

CPAN (pending PAUSE approval)

Core modules only (IO::Socket::SSL, JSON::PP, HTTP::Tiny).

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

## Install

```
# single file, core modules only (JSON::PP ships with Perl 5.14+, IO::Socket::SSL with 5.20+)
curl -O https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/perl/etunnel.pl

# or from a source checkout (installs bin/etunnel.pl + lib/etunnel.pl):
git clone https://github.com/topcheer/etunnel && cd etunnel/sdks/perl && perl Makefile.PL && make && sudo make install
```

Requires Perl 5.20+. **Not on CPAN**: `cpanm etunnel` fails (no such
distribution - the PAUSE upload is pending); install from source as above.
`make dist` builds `etunnel-<version>.tar.gz` for the future CPAN release.

## AI agent quick-start (copy this prompt)

````text
Integrate etunnel (Perl SDK) into this project to expose the local service on port <PORT> at a public HTTPS URL.

- Package: none on CPAN (`cpanm etunnel` fails - PAUSE pending) · Install: `curl -O https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/perl/etunnel.pl` (core modules only, Perl >= 5.20)
- Server: wss://t.etun.uk/tunnel (override with ETUNNEL_SERVER). Anonymous tier = random subdomain, 4h TTL; custom subdomain / longer TTL need a paid token (opts.token or ETUNNEL_TOKEN).
- Minimal embed (verified against the SDK source):
```perl
use strict;
use warnings;
$| = 1;
require './etunnel.pl';
my $t = etunnel_connect('wss://t.etun.uk/tunnel', '127.0.0.1:3000', {});
print "TUNNEL_URL: $t->{url}\n";
sleep 3600;
```
- Rules: print the public URL as `TUNNEL_URL: <url>` and keep the process alive; call `$t->{close}->()` on shutdown to release the subdomain. This SDK is buffer-based (no WebSocket upgrades, no streaming) - fine for webhooks/APIs/dashboards.
- Do not invent other options. Full API/options: #embed and #multi-target-routing. Wire protocol: https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md
````

## CLI

```
perl etunnel.pl 3000
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

```perl
use strict;
use warnings;
$| = 1;                    # unbuffered stdout: the URL shows up in pipes and logs too
require './etunnel.pl';    # defines etunnel_connect(); the CLI block is skipped on require

my $t = etunnel_connect('wss://t.etun.uk/tunnel', '127.0.0.1:3000', {});
print "TUNNEL_URL: $t->{url}\n";

sleep 3600;                # the tunnel closes when the process exits
# on shutdown: $t->{close}->()   (sends `bye`, frees the subdomain)
```

`etunnel_connect($server, "host:port", \%opts)` blocks until `welcome` and
returns a hashref: `url`, `resumeToken` (replay it via `resume =>` to reclaim
the same subdomain), `close` (coderef, sends `bye`). A bare port is accepted as
`127.0.0.1:port`; `local` may be `undef` when `routes` is given. A `threads`
reader thread serves requests from then on - no pump call is needed, the
process just has to stay alive.

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

```perl
my $t = etunnel_connect("wss://t.example.com/tunnel", undef, {
    routes => [
        { prefix => "/api", addr => "127.0.0.1:8080", strip => 1 },
        { prefix => "/",    addr => "127.0.0.1:3000" },
    ],
});
```

Semantics (identical in every SDK, pinned by tests): longest prefix
wins; matching is on segment boundaries (`/api` never matches `/apiv2`);
`strip` removes only the path part - the query string is carried over
byte-for-byte, an exact hit bottoms out at `/`; no matching route (only
possible without a `/` fallback) answers `404` at the SDK edge.

> Redirect note: with `strip`, a backend 302 to an absolute path (`/users/next`) lands on the fallback route on the next hop - backends should return relative redirects or keep the prefix (same as nginx `proxy_pass` with trailing slash; see #155).
