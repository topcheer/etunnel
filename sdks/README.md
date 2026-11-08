# etunnel SDKs — 20 languages, one wire protocol

Expose a local service to the internet through an etunnel server
(`wss://t.ggcode.uk/tunnel`) — instant public HTTPS, automatic TLS, zero
signup. Every SDK speaks the same tiny protocol
([PROTOCOL.md](../PROTOCOL.md)); the **Go SDK** is the canonical streaming
implementation with full WebSocket pass-through, the other 19 are
buffer-based ports (webhook/dashboard friendly; use Go for WS upgrade).

## Install matrix

| # | Language | Package | Install | Registry |
|---|----------|---------|---------|----------|
| 0 | [Go](../sdk/) (canonical) | `github.com/topcheer/etunnel` | `go get github.com/topcheer/etunnel` | [Go proxy](https://pkg.go.dev/github.com/topcheer/etunnel) |
| 1 | [Node.js](node/) | `@easytunnel/et-sdk` | `npm install @easytunnel/et-sdk` | [npm](https://www.npmjs.com/package/@easytunnel/et-sdk) |
| 2 | [TypeScript](typescript/) | source-only (`etunnel.ts`) | copy `etunnel.ts`, or `npm install @easytunnel/et-sdk` | [npm (JS)](https://www.npmjs.com/package/@easytunnel/et-sdk) |
| 3 | [Deno](deno/) | `@ggcode/etunnel` | `deno add @ggcode/etunnel` | [JSR](https://jsr.io/@ggcode/etunnel) |
| 4 | [Python](python/) | `etunnel` | `pip install etunnel` | [PyPI](https://pypi.org/project/etunnel/) |
| 5 | [Java](java/) | `dev.ggcode:etunnel` | Maven Central snippet below | [Maven Central](https://central.sonatype.com/artifact/dev.ggcode/etunnel) |
| 6 | [Kotlin](kotlin/) | via Java artifact | see [java/](java/) | Maven Central |
| 7 | [Scala](scala/) | via Java artifact | see [java/](java/) | Maven Central |
| 8 | [C#](csharp/) | `etunnel` | `dotnet add package etunnel` | [NuGet](https://www.nuget.org/packages/etunnel) |
| 9 | [Rust](rust/) | `etunnel` | `cargo install etunnel` | [crates.io](https://crates.io/crates/etunnel) |
| 10 | [Swift](../Package.swift) | SPM executable | `.package(url: "https://github.com/topcheer/etunnel", from: "1.0.0")` | [SwiftPM](https://github.com/topcheer/etunnel) |
| 11 | [Dart](dart/) | `etunnel` | `dart pub add etunnel` | [pub.dev](https://pub.dev/packages/etunnel) |
| 12 | [Ruby](ruby/) | `etunnel` | `gem install etunnel` | [RubyGems](https://rubygems.org/gems/etunnel) |
| 13 | [PHP](../composer.json) | `ggcode/etunnel` | `composer require ggcode/etunnel` | [Packagist](https://packagist.org/packages/ggcode/etunnel) |
| 14 | [Perl](perl/) | `etunnel` | `cpanm etunnel` | CPAN (pending) |
| 15 | [Clojure](clojure/) | `dev.ggcode/etunnel` | deps.edn snippet in [clojure/](clojure/) | [Clojars](https://clojars.org/dev.ggcode/etunnel) |
| 16 | [Elixir](elixir/) | `etunnel` | `{:etunnel, "~> 1.0"}` | [Hex](https://hex.pm/packages/etunnel) |
| 17 | [Lua](lua/) | `ggcode.etunnel` | `luarocks install ggcode.etunnel` | [LuaRocks](https://luarocks.org/modules/topcheer/ggcode.etunnel) |
| 18 | [C](c/) | source tarball | `make` (see [c/](c/)) | [Releases](https://github.com/topcheer/etunnel/releases) |
| 19 | [C++](cpp/) | source tarball | `cmake` (see [cpp/](cpp/)) | [Releases](https://github.com/topcheer/etunnel/releases) |

Java coordinates:

```xml
<dependency>
  <groupId>dev.ggcode</groupId>
  <artifactId>etunnel</artifactId>
  <version>1.1.0</version>
</dependency>
```

## 60-second tour

```sh
# any SDK CLI works the same way:
etunnel 3000                      # or: npx @easytunnel/et-sdk 3000
# TUNNEL_URL: https://quiet-frog-42.t.ggcode.uk   <- now live
```

Embedding (Node example; every SDK README has its own):

```js
const { connect } = require('@easytunnel/et-sdk');
const t = await connect('wss://t.ggcode.uk/tunnel', 3000);
console.log(t.url);
```

## Choosing an SDK

- **WebSockets / streaming bodies / long-lived SSE** → [Go SDK](../sdk/)
  (101-upgrade pass-through, verified end-to-end on the live server)
- **Webhooks, dashboards, demos, CI callbacks** → any language above
  (buffered request/response per stream)
- Environment variables everywhere: `ETUNNEL_SERVER`, `ETUNNEL_LOCAL`,
  `ETUNNEL_TOKEN`, `ETUNNEL_SUBDOMAIN`

## Server

Single Go binary; see the [main README](../README.md) for a 60-second setup
and the public instance at `wss://t.ggcode.uk/tunnel`.
