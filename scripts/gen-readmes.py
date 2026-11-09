#!/usr/bin/env python3
"""Generate per-SDK README.md files for all 20 language SDKs."""
import pathlib

TMPL = """# etunnel for {title}

{badge}

{desc}

Zero registration: every SDK client speaks the same tiny wire protocol
([PROTOCOL.md](../../PROTOCOL.md)) against any etunnel server
(default `wss://t.etun.uk/tunnel`).

## Install

```
{install}
```

{req}

## CLI

```
{cli}
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

{embed}

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
"""
SEE = "## Embed\n\nSee the header of the source file for the embed example."
R = {

 "node": ("Node.js", "Expose a local service through an etunnel reverse-tunnel server. Zero dependencies (Node >= 22 built-in WebSocket).", "npm install @easytunnel/et-sdk", "Requires Node >= 22.", "npx @easytunnel/et-sdk 3000", "## Embed\n\n```js\nconst { connect } = require('@easytunnel/et-sdk');\nconst t = await connect('wss://t.etun.uk/tunnel', 3000);\nconsole.log(t.url);\n```"),
 "typescript": ("TypeScript", "Same implementation as the Node SDK, with types.", "# etunnel-ts is source-only (not on npm yet):\n# copy etunnel.ts into your project, or use the published JS package\nnpm install @easytunnel/et-sdk", "Node >= 22 (type stripping) or tsc.", "node etunnel.ts 3000", "## Embed\n\n```ts\n// after copying etunnel.ts next to your code:\nimport { connect } from './etunnel.ts';\nconst t = await connect('wss://t.etun.uk/tunnel', 3000);\nconsole.log(t.url);\n```"),
 "deno": ("Deno", "Built-in WebSocket, zero dependencies.", "deno add @ggcode/etunnel", "Deno >= 1.38.", "deno run --allow-net etunnel.ts 3000", "## Embed\n\n```ts\nimport { connect } from '@ggcode/etunnel';\nconst t = await connect('wss://t.etun.uk/tunnel', 3000);\n```"),
 "python": ("Python", "websocket-client based.", "pip install etunnel", "Python >= 3.8 (websocket-client installed automatically).", "etunnel 3000", "## Embed\n\n```python\nfrom etunnel import connect\nt = connect(\"wss://t.etun.uk/tunnel\", 3000, {})\nprint(t[\"url\"])\n```"),
 "java": ("Java", "Single-file SDK, JDK 11+ built-in HttpClient/WebSocket — zero dependencies. Published to Maven Central.", "Maven:\n<dependency>\n  <groupId>dev.ggcode</groupId>\n  <artifactId>etunnel</artifactId>\n  <version>1.0.0</version>\n</dependency>\n\nGradle: implementation \"dev.ggcode:etunnel:1.0.0\"", "Requires JDK 11+.", "java sdks/java/src/main/java/dev/ggcode/Etunnel.java 3000", SEE),
 "kotlin": ("Kotlin", "Single-file SDK on JDK HttpClient/WebSocket.", "kotlinc Etunnel.kt -include-runtime -d etunnel.jar", "Requires JDK 11+.", "java -jar etunnel.jar 3000", SEE),
 "scala": ("Scala", "Single-file SDK on JDK HttpClient/WebSocket.", "scala Etunnel.scala 3000", "Requires JDK 11+ and Scala 2.13/3.", "scala Etunnel.scala 3000", SEE),
 "csharp": ("C# / .NET", "System.Net.WebSockets + HttpClient, zero NuGet dependencies.", "dotnet add package etunnel", "Requires .NET 8+ (tested on 10).", "dotnet run Etunnel.cs 3000", SEE),
 "rust": ("Rust", "tungstenite + ureq CLI crate.", "cargo install etunnel", "Requires Rust 1.70+.", "etunnel 3000", SEE),
 "swift": ("Swift", "Foundation-only single-file SDK (URLSession WebSocket).", "swift build   # SPM: .package(url:..., from: \"1.0.0\")", "Requires Swift 5.9+.", "swift etunnel.swift 3000", SEE),
 "dart": ("Dart", "dart:io only, zero dependencies.", "dart pub add etunnel", "Requires Dart ^3.0.", "dart run etunnel.dart 3000", SEE),
 "ruby": ("Ruby", "EventMachine/faye-websocket based.", "gem install etunnel", "Requires Ruby >= 2.7 (pulls in faye-websocket).", "etunnel 3000", "## Embed\n\n```ruby\nrequire 'etunnel'\nt = Etunnel.connect('wss://t.etun.uk/tunnel', 3000)\nputs t.url\n```"),
 "php": ("PHP", "Streams + ext-openssl only — no curl/guzzle needed.", "composer require ggcode/etunnel", "Requires PHP >= 8.0 with ext-openssl.", "php etunnel.php 3000", SEE),
 "perl": ("Perl", "Core modules only (IO::Socket::SSL, JSON::PP, HTTP::Tiny).", "cpanm etunnel", "Requires Perl 5.16+.", "perl etunnel.pl 3000", SEE),
 "clojure": ("Clojure", "JDK HttpClient/WebSocket + cheshire.", ";; deps.edn\n{:deps {dev.ggcode/etunnel-clj {:mvn/version \"1.0.0\"}}}", "Requires JDK 11+.", "clojure -M -m etunnel 3000", "## Embed\n\n```clojure\n(def t (et/connect! \"wss://t.etun.uk/tunnel\" 3000))\n(println (:url t))\n```"),
 "elixir": ("Elixir", "websockex + jason; local requests via :httpc.", "# mix.exs\ndefp deps do\n  [{:etunnel, \"~> 1.0\"}]\nend", "Requires Elixir >= 1.12 / OTP 24+.", "mix run --no-halt runner.exs 3000", "## Embed\n\n```elixir\n{:ok, t} = Etunnel.start_link(server: \"wss://t.etun.uk/tunnel\", local: 3000)\nEtunnel.url(t)\n```"),
 "lua": ("Lua", "luasocket + luasec (wss against autocert servers needs lua-http for SNI).", "luarocks install ggcode.etunnel", "Requires Lua >= 5.2.", "lua etunnel.lua 3000", SEE),
 "c": ("C", "libwebsockets + libcurl + json-c.", "make && sudo make install", "Requires libwebsockets, libcurl, json-c.", "etunnel 3000", SEE),
 "cpp": ("C++", "websocketpp + asio + libcurl.", "cmake -B build && cmake --build build", "Requires websocketpp, asio (or Boost >= 1.66), libcurl, OpenSSL.", "./build/etunnel 3000", SEE),
}


BADGES = {
 "node": "[![npm](https://img.shields.io/npm/v/@easytunnel/et-sdk?label=npm%20%40easytunnel%2Fet-sdk)](https://www.npmjs.com/package/@easytunnel/et-sdk)",
 "typescript": "[![npm](https://img.shields.io/npm/v/@easytunnel/et-sdk?label=npm%20%40easytunnel%2Fet-sdk)](https://www.npmjs.com/package/@easytunnel/et-sdk) (JS runtime for the same wire protocol)",
 "deno": "[![JSR](https://img.shields.io/badge/JSR-@ggcode%2Fetunnel-f7df1e?logo=deno)](https://jsr.io/@ggcode/etunnel)",
 "python": "[![PyPI](https://img.shields.io/pypi/v/etunnel?label=PyPI%20etunnel)](https://pypi.org/project/etunnel/)",
 "java": "[![Maven Central](https://img.shields.io/maven-central/v/dev.ggcode/etunnel?label=Maven%20Central)](https://central.sonatype.com/artifact/dev.ggcode/etunnel)",
 "kotlin": "Maven Central: `dev.ggcode:etunnel` (Java artifact)",
 "scala": "Maven Central: `dev.ggcode:etunnel` (Java artifact)",
 "csharp": "[![NuGet](https://img.shields.io/nuget/v/etunnel?label=NuGet%20etunnel)](https://www.nuget.org/packages/etunnel)",
 "rust": "[![crates.io](https://img.shields.io/crates/v/etunnel?label=crates.io%20etunnel)](https://crates.io/crates/etunnel)",
 "swift": "[![SwiftPM](https://img.shields.io/badge/SwiftPM-compatible-orange?logo=swift)](https://github.com/topcheer/etunnel)",
 "dart": "[![pub.dev](https://img.shields.io/pub/v/etunnel?label=pub.dev%20etunnel)](https://pub.dev/packages/etunnel)",
 "ruby": "[![RubyGems](https://img.shields.io/gem/v/etunnel?label=RubyGems%20etunnel)](https://rubygems.org/gems/etunnel)",
 "php": "[![Packagist](https://img.shields.io/packagist/v/ggcode/etunnel)](https://packagist.org/packages/ggcode/etunnel)",
 "perl": "CPAN (pending PAUSE approval)",
 "clojure": "[![Clojars](https://img.shields.io/badge/Clojars-dev.ggcode%2Fetunnel-clj-5881d8?logo=clojure)](https://clojars.org/dev.ggcode/etunnel-clj)",
 "elixir": "[![Hex](https://img.shields.io/hexpm/v/etunnel?label=Hex%20etunnel)](https://hex.pm/packages/etunnel)",
 "lua": "[![LuaRocks](https://img.shields.io/badge/LuaRocks-ggcode.etunnel-8b4bb8?logo=lua)](https://luarocks.org/modules/topcheer/ggcode.etunnel)",
 "c": "[![GitHub release](https://img.shields.io/github/v/release/topcheer/etunnel?label=GitHub%20release)](https://github.com/topcheer/etunnel/releases)",
 "cpp": "[![GitHub release](https://img.shields.io/github/v/release/topcheer/etunnel?label=GitHub%20release)](https://github.com/topcheer/etunnel/releases)",
}

root = pathlib.Path(__file__).resolve().parent.parent
for lang, (title, desc, install, req, cli, embed) in R.items():
    p = root / "sdks" / lang / "README.md"
    p.write_text(TMPL.format(title=title, badge=BADGES[lang], desc=desc, install=install, req=req, cli=cli, embed=embed))
    print("wrote", p.relative_to(root), p.stat().st_size, "bytes")
print("total:", len(R))
