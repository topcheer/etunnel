# etunnel for Dart

[![pub.dev](https://img.shields.io/pub/v/etunnel?label=pub.dev%20etunnel)](https://pub.dev/packages/etunnel)

dart:io only, zero dependencies.

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

## Install

```
dart pub add etunnel
```

Requires Dart ^3.0, zero dependencies. **Status 1.7.35:** the pub.dev archive
ships `etunnel.dart` at the package root instead of `lib/`, so
`import 'package:etunnel/etunnel.dart'` cannot resolve. Until it is
republished, use the single file:
`curl -O https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/dart/etunnel.dart`
and `import 'etunnel.dart';`.

## AI agent quick-start (copy this prompt)

````text
Integrate etunnel (Dart SDK) into this project to expose the local service on port <PORT> at a public HTTPS URL.

- Package: `etunnel` on pub.dev (Dart >= 3; the 1.7.35 archive is unusable: no lib/) · Install: `curl -O https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/dart/etunnel.dart` and import it as a relative file
- Server: wss://t.etun.uk/tunnel (override with ETUNNEL_SERVER). Anonymous tier = random subdomain, 4h TTL; custom subdomain / longer TTL need a paid token (opts.token or ETUNNEL_TOKEN).
- Minimal embed (verified against the SDK source):
```dart
import 'dart:io';
import 'etunnel.dart';
Future<void> main() async {
  final t = await Etunnel.connect('wss://t.etun.uk/tunnel', '3000');
  print('TUNNEL_URL: ${t.url}');
  ProcessSignal.sigint.watch().listen((_) { t.close(); exit(0); });
}
```
- Rules: print the public URL as `TUNNEL_URL: <url>` and keep the process alive; call `t.close()` on shutdown to release the subdomain. This SDK is buffer-based (no WebSocket upgrades, no streaming) - fine for webhooks/APIs/dashboards.
- Do not invent other options. Full API/options: #embed and #multi-target-routing. Wire protocol: https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md
````

## CLI

```
dart run etunnel.dart 3000
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

```dart
import 'dart:io';
import 'etunnel.dart';   // the single file, next to this program

Future<void> main() async {
  final t = await Etunnel.connect('wss://t.etun.uk/tunnel', '3000');
  print('TUNNEL_URL: ${t.url}');

  ProcessSignal.sigint.watch().listen((_) { t.close(); exit(0); });
}
```

`Etunnel.connect(server, local, {token, subdomain, resume, routes})` returns a
`Future<Etunnel>`; `local` is `'3000'`, `'host:port'`, or `null` when `routes`
is given. `t.url` (public URL), `t.resumeToken` (replay via `resume:` to
reclaim the same subdomain), `t.close([reason])` (sends `bye`, `void`).

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

```dart
final t = await Etunnel.connect("wss://t.example.com/tunnel", null, routes: [
  Route("/api", "127.0.0.1:8080", strip: true),
  Route("/", "127.0.0.1:3000"),
]);
```

Semantics (identical in every SDK, pinned by tests): longest prefix
wins; matching is on segment boundaries (`/api` never matches `/apiv2`);
`strip` removes only the path part - the query string is carried over
byte-for-byte, an exact hit bottoms out at `/`; no matching route (only
possible without a `/` fallback) answers `404` at the SDK edge.

> Redirect note: with `strip`, a backend 302 to an absolute path (`/users/next`) lands on the fallback route on the next hop - backends should return relative redirects or keep the prefix (same as nginx `proxy_pass` with trailing slash; see #155).
