# etunnel SDKs — 20 languages, all live-verified

One line of code, no registration. The server is a single Go binary — every
SDK below is a *client* speaking the same wire protocol
([PROTOCOL.md](../PROTOCOL.md)), so no language runtime is ever needed on the
server side.

| # | Language | Path | Client deps | Verification |
|---|----------|------|-------------|--------------|
| 1 | Go | [`/sdk`](../sdk) (canonical, streaming + WS passthrough) | none | live wss, race-tested |
| 2 | Python | `python/etunnel.py` | `pip install websocket-client` | live wss |
| 3 | JavaScript (Node) | `node/etunnel.js` | none (Node ≥ 22) | live wss |
| 4 | TypeScript | `typescript/etunnel.ts` | none (Node ≥ 22) | live wss |
| 5 | Deno | `deno/etunnel.ts` | none | live wss |
| 6 | Java | `java/Etunnel.java` | none (JDK 11+) | live wss |
| 7 | Kotlin | `kotlin/Etunnel.kt` | none (JDK 11+) | live wss |
| 8 | C# | `csharp/Etunnel.cs` | none (.NET file-based app) | live wss |
| 9 | Rust | `rust/` (cargo) | tungstenite, ureq | live wss |
| 10 | Swift | `swift/etunnel.swift` | none (Foundation) | live wss |
| 11 | Dart | `dart/etunnel.dart` | none (dart:io) | live wss |
| 12 | Ruby | `ruby/etunnel.rb` | `gem install faye-websocket` | live wss |
| 13 | PHP | `php/etunnel.php` | ext-openssl only (no curl needed) | live wss |
| 14 | Perl | `perl/etunnel.pl` | core modules only | live wss |
| 15 | Scala | `scala/Etunnel.scala` | none (JDK 11+) | live wss |
| 16 | Clojure | `clojure/` | cheshire (deps.edn) | live wss |
| 17 | C++ | `cpp/etunnel.cpp` | websocketpp, asio, libcurl | live wss |
| 18 | C | `c/etunnel.c` | libwebsockets, libcurl, json-c | live wss |
| 19 | Lua | `lua/etunnel.lua` | luasocket + luasec (wss needs lua-http for SNI) | live ws (protocol full path); wss handshake verified against autocert SNI server via lua-http only |
| 20 | Elixir | `elixir/` (mix) | websockex, jason | live ws (protocol full path); wss handshake verified — public HTTPS check pending Let's Encrypt quota reset (50 certs/registered-domain/week hit by testing) |

"live wss" = the SDK ran as a real client against `wss://t.ggcode.uk/tunnel`,
received its assigned subdomain, and an external `curl https://<sub>.t.ggcode.uk`
returned the local service's body. Lua and Elixir were verified the same way
against a local `ws://` etunneld instance (full protocol path: hello → welcome
→ req → res → body frames → FIN), because: LuaSec has no client SNI (needs
[lua-http](https://github.com/daurnimator/lua-http)); and the Elixir public
HTTPS check collided with the Let's Encrypt 50-certs/week rate limit that the
day's 80+ test certificates exhausted (resets weekly; the tunnel itself
registered and answered fine over wss).

## Usage (identical shape everywhere)

All SDKs honor the same environment variables:

- `ETUNNEL_SERVER` — tunnel server (default `wss://t.ggcode.uk/tunnel`)
- `ETUNNEL_LOCAL` — local port or `host:port` (or first CLI arg)
- `ETUNNEL_TOKEN` — paid-plan API key (optional)
- `ETUNNEL_SUBDOMAIN` — custom subdomain (requires a paid plan)
- `ETUNNEL_DEBUG=1` — verbose protocol trace (where supported)

CLI form (each prints `TUNNEL_URL: https://<subdomain>.t.ggcode.uk`):

```sh
python3 python/etunnel.py 3000
node node/etunnel.js 3000
java java/Etunnel.java 3000
dotnet run csharp/Etunnel.cs 3000
dart run dart/etunnel.dart 3000
swift swift/etunnel.swift 3000
perl perl/etunnel.pl 3000
php php/etunnel.php 3000
ruby ruby/etunnel.rb 3000          # gem install faye-websocket
scala scala/Etunnel.scala 3000     # needs javac/scala-compiler
kotlinc kotlin/Etunnel.kt -include-runtime -d kt.jar && java -jar kt.jar 3000
(cd clojure && clojure -M -m etunnel 3000)
(cd rust && cargo run --release 3000)
g++ -std=c++17 -DASIO_STANDALONE cpp/etunnel.cpp -o et -lcurl -lssl -lcrypto -lpthread && ./et 3000
gcc c/etunnel.c -o et -lwebsockets -lcurl -ljson-c && ./et 3000
lua lua/etunnel.lua 3000           # wss needs lua-http; ws works with luasocket/luasec
(cd elixir && mix deps.get && mix run --no-halt runner.exs 3000)
```

## Implementation notes

- All non-Go SDKs are **buffer-based** protocol clients: each request/response
  body is buffered per stream, then forwarded to the local service. This keeps
  every implementation dependency-light and is fine for webhooks, dashboards,
  and demos. The Go SDK (`/sdk`) is the reference implementation with full
  streaming bodies and WebSocket (101 upgrade) pass-through — use it when you
  need true streaming or WS forwarding through the tunnel.
- The wire protocol is intentionally tiny (JSON text control messages +
  9-byte-header binary frames) so a complete client fits in a single file in
  any language. See [PROTOCOL.md](../PROTOCOL.md).
- Notable portability findings from live testing: Python's websocket-client
  sends binary frames with the text opcode (the server content-sniffs and
  tolerates this for any client); LuaSec lacks client-side SNI; JDK
  `WebSocket.Builder.subprotocols` requires ≥ 2 args; Clojure/cheshire parses
  small JSON ints as Integer while frame parsing yields Long (keys must be
  normalized); JVM `HttpClient` must be `NO_PROXY` for loopback on machines
  with system proxies.
