#!/usr/bin/env python3
"""Generate the per-language SDK READMEs from one template.

Every package README has the same shape (badge, diagram, install, embed, CLI,
API, options, env vars, limits, troubleshooting) so the 20 SDKs read as one
product. Run after changing an SDK's API or a published version:

    python3 scripts/gen_sdk_readmes.py

The generated files are committed; this script only keeps them in sync.
"""

from __future__ import annotations

import pathlib

ROOT = pathlib.Path(__file__).resolve().parent.parent
VERSION = (ROOT / "VERSION").read_text().strip()
DEFAULT_SERVER = "wss://t.etun.uk/tunnel"
REPO = "https://github.com/topcheer/etunnel"

TROUBLE = """\
| Symptom | Cause | Fix |
|---|---|---|
| `ws: cannot connect to ...` | DNS, corporate proxy or firewall blocks the outbound socket | `curl -sI https://t.etun.uk/` from the same host; set `HTTPS_PROXY` if you need one |
| Public URL answers `404 - no tunnel here` | The tunnel process exited, or the plan TTL expired | Restart the client; the subdomain is released on disconnect |
| `error: invalid_token` | `token` set but not a paid-plan API key | Copy the key from `/me` on the portal, or drop the token for an anonymous tunnel |
| `error: too_many_tunnels` | Per-IP anonymous tunnel cap on the server | Close old clients (`lsof -ti:PORT`), or use a paid plan with a token |
| `error: subdomain_taken` | Custom subdomain already claimed by another account | Pick another name, or remove it from the old account in `/me` |
| Request hangs, then `502` | Local service is not listening on the requested port | `curl -sv http://127.0.0.1:3000/` first |
| WebSocket endpoints fail through the tunnel | Buffer-based SDK (all but Go) cannot forward 101 upgrades | Use the [Go SDK](../../sdk) for WebSocket pass-through |"""

ENV = """\
| Variable | Meaning | Default |
|---|---|---|
| `ETUNNEL_SERVER` | Tunnel server WebSocket URL | `wss://t.etun.uk/tunnel` |
| `ETUNNEL_LOCAL` | Local port or `host:port` (the first CLI argument wins) | `3000` |
| `ETUNNEL_TOKEN` | Paid-plan API key (enables custom subdomain + longer TTL) | - |
| `ETUNNEL_SUBDOMAIN` | Requested subdomain, paid plans only | random |"""

L: list[dict] = [
    {
        "dir": "node",
        "name": "Node.js",
        "badges": "[![npm](https://img.shields.io/npm/v/@easytunnel/et-sdk?label=npm%20%40easytunnel%2Fet-sdk)](https://www.npmjs.com/package/@easytunnel/et-sdk)\n[![Node](https://img.shields.io/badge/node-%3E%3D22-brightgreen)](https://nodejs.org)",
        "blurb": "Expose a local service on the public internet from Node: one `await`, no dependencies (Node >= 22 ships a global `WebSocket`).",
        "lang": "js",
        "install": "npm install @easytunnel/et-sdk",
        "embed": """\
const { connect } = require('@easytunnel/et-sdk');

const tunnel = await connect('%(server)s', 3000);
console.log('public URL:', tunnel.url);

// keep the process alive; close() sends a clean bye to the server
process.on('SIGINT', () => { tunnel.close(); process.exit(0); });""",
        "cli": "npx @easytunnel/et-sdk 3000",
        "api": "| `connect(server, local, opts?)` | `Promise<Tunnel>` - resolves after the server's `welcome` frame |\n| `Tunnel.url` | `string` - public HTTPS URL |\n| `Tunnel.close(reason?)` | closes the socket, freeing the subdomain |",
        "opts": "`{ token, subdomain, client, onClose }` - `client` only overrides the user agent sent in `hello`.",
        "notes": "- Buffer-based client: request/response bodies are buffered per stream. Fine for webhooks, dashboards and APIs; use the [Go SDK](../../sdk) for streaming or WebSocket upgrades.\n- CommonJS and ESM both work (`import { connect } from '@easytunnel/et-sdk'`).",
    },
    {
        "dir": "typescript",
        "name": "TypeScript",
        "badges": "[![npm](https://img.shields.io/npm/v/@easytunnel/et-sdk?label=npm%20%40easytunnel%2Fet-sdk)](https://www.npmjs.com/package/@easytunnel/et-sdk)\n[![types](https://img.shields.io/badge/types-included-blue)](etunnel.ts)",
        "blurb": "The same client as the Node SDK, typed and dependency-free: `etunnel.ts` is a single file you can vendor, or import the npm package.",
        "lang": "ts",
        "install": "# option A - single file, no build step (Node >= 22.6 runs .ts directly)\ncurl -O %(repo)s/raw/main/sdks/typescript/etunnel.ts\n\n# option B - npm package (ships the compiled JS + types)\nnpm install @easytunnel/et-sdk",
        "embed": """\
import { connect } from './etunnel.ts';   // or '@easytunnel/et-sdk'

const tunnel = await connect('%(server)s', 3000);

console.log('public URL:', tunnel.url);
console.log('plan:', tunnel.plan);""",
        "cli": "node etunnel.ts 3000",
        "api": "| `connect(server, local, opts?)` | `Promise<Tunnel>` |\n| `Tunnel.url` / `.hostname` / `.subdomain` / `.plan` | connection facts from the `welcome` frame |\n| `Tunnel.close(reason?)` | graceful shutdown |",
        "opts": "`Options { token?, subdomain?, client?, onClose? }`",
        "notes": "- Buffer-based client (no WS upgrades). Use the [Go SDK](../../sdk) when you need streaming or WebSocket pass-through.\n- Type-checked with `strict`; targets Node >= 22 (global `WebSocket`) and Deno via the [Deno port](../deno/).",
    },
    {
        "dir": "deno",
        "name": "Deno",
        "badges": "[![JSR](https://img.shields.io/badge/JSR-%40ggcode%2Fetunnel-f7df1e?logo=deno)](https://jsr.io/@ggcode/etunnel)\n[![JSR score](https://jsr.io/badges/@ggcode/etunnel/score)](https://jsr.io/@ggcode/etunnel)",
        "blurb": "Zero-dependency Deno client, published on JSR: permissions are limited to `--allow-net`.",
        "lang": "ts",
        "install": "deno add jsr:@ggcode/etunnel\n# or import straight from the registry:\n#   import { connect } from 'jsr:@ggcode/etunnel';",
        "embed": """\
import { connect } from 'jsr:@ggcode/etunnel';

const tunnel = await connect('%(server)s', 3000);
console.log('public URL:', tunnel.url);

Deno.addSignalListener('SIGINT', () => {
  tunnel.close();
  Deno.exit(0);
});""",
        "cli": "deno run --allow-net https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/deno/etunnel.ts 3000",
        "api": "| `connect(server, local, opts?)` | `Promise<Tunnel>` |\n| `Tunnel.url` / `.hostname` / `.subdomain` / `.plan` | facts from the `welcome` frame |\n| `Tunnel.close(reason?)` | graceful shutdown |",
        "opts": "`Options { token?, subdomain?, client?, onClose? }`",
        "notes": "- Buffer-based client (no WS upgrades); the [Go SDK](../../sdk) handles those.\n- Only needs `--allow-net`; no `--allow-read` or `--allow-run`.",
    },
    {
        "dir": "python",
        "name": "Python",
        "badges": "[![PyPI](https://img.shields.io/pypi/v/etunnel?label=PyPI%20etunnel)](https://pypi.org/project/etunnel/)\n[![Python](https://img.shields.io/badge/python-3.8%2B-blue)](https://pypi.org/project/etunnel/)",
        "blurb": "`connect()` returns as soon as the server assigns a URL; the tunnel then serves requests on a background thread.",
        "lang": "python",
        "install": "pip install etunnel\n# the only dependency is websocket-client, installed for you",
        "embed": """\
from etunnel import connect

# port number or "host:port"
tunnel = connect("%(server)s", 3000)
print("public URL:", tunnel["url"])

try:
    input("press Enter to stop\\n")
finally:
    tunnel["close"]()""",
        "cli": "etunnel 3000        # installed as a console script\npython -m etunnel 3000",
        "api": "| `connect(server, local, opts=None)` | `dict` with `url`, `hostname`, `subdomain`, `plan`, `close` |\n| `tunnel[\"close\"]()` | stops the worker thread and sends `bye` |\n| `opts` | `{token, subdomain, client, on_close}` - `on_close(reason)` fires on server-side disconnects |",
        "opts": "`opts={token=\"et_...\", subdomain=\"myapp\", on_close=fn}`",
        "notes": "- Buffer-based client (no WS upgrades); the [Go SDK](../../sdk) handles those.\n- Thread-safe: the background thread only touches its own stream table.\n- Works under gunicorn/uvicorn as long as the process outlives the tunnel.",
    },
    {
        "dir": "go",
        "name": "Go",
        "badges": "[![Go](https://img.shields.io/github/go-mod/go-version/topcheer/etunnel?label=Go%20module)](https://pkg.go.dev/github.com/topcheer/etunnel/sdk)\n[![Go Reference](https://pkg.go.dev/badge/github.com/topcheer/etunnel/sdk.svg)](https://pkg.go.dev/github.com/topcheer/etunnel/sdk)",
        "blurb": "The reference client: full HTTP/1.1 streaming and WebSocket pass-through, one import.",
        "lang": "go",
        "install": "go get github.com/topcheer/etunnel/sdk",
        "embed": """\
package main

import (
\t"context"
\t"log"

\t"github.com/topcheer/etunnel/sdk"
)

func main() {
\ttunnel, err := etunnel.Connect(context.Background(), etunnel.Options{
\t\tServer:    "%(server)s",
\t\tLocalAddr: "127.0.0.1:3000",
\t})
\tif err != nil {
\t\tlog.Fatal(err)
\t}

\tlog.Printf("public URL: %%s", tunnel.URL())
\tdefer tunnel.Close("app exiting")

\tselect {} // or run your own server; the tunnel is a background client
}""",
        "cli": "go install github.com/topcheer/etunnel/cmd/etunnel@latest\netunnel -server wss://t.etun.uk/tunnel 3000",
        "api": "| `Connect(ctx, Options)` | `*Tunnel`, error |\n| `Tunnel.URL()` / `Hostname()` / `Subdomain()` / `Plan()` / `ExpiresAt()` | connection facts |\n| `Tunnel.Done()` | channel closed when the server drops the tunnel (TTL expiry, kick) |\n| `Tunnel.Close(reason)` | graceful shutdown |",
        "opts": "`Options{Server, LocalAddr, Subdomain, Token, Client, Logger, OnClose}`",
        "notes": "- **Streaming**: response bodies are relayed chunk by chunk, flushes are preserved, `Transfer-Encoding: chunked` works.\n- **WebSocket pass-through**: a `101 Upgrade` hijacks both sides into a raw byte pipe, so `wss://<sub>.t.etun.uk` reaches your local WS server.\n- Hop-by-hop headers (`Connection`, `Transfer-Encoding`, ...) are stripped in both directions; `X-Forwarded-For` / `X-Forwarded-Proto` are set by the server.",
    },
    {
        "dir": "java",
        "name": "Java",
        "badges": "[![Maven Central](https://img.shields.io/maven-central/v/dev.ggcode/etunnel?label=Maven%20Central)](https://central.sonatype.com/artifact/dev.ggcode/etunnel)\n[![JDK](https://img.shields.io/badge/JDK-11%2B-orange)](https://central.sonatype.com/artifact/dev.ggcode/etunnel)",
        "blurb": "Zero-dependency client built on `java.net.http.WebSocket` - works on JDK 11 and later, no Netty, no Jackson.",
        "lang": "java",
        "install": f"""\
Maven:
<dependency>
  <groupId>dev.ggcode</groupId>
  <artifactId>etunnel</artifactId>
  <version>{VERSION}</version>
</dependency>

Gradle:
implementation "dev.ggcode:etunnel:{VERSION}\"""",
        "embed": """\
import dev.ggcode.Etunnel;

public class Main {
    public static void main(String[] args) throws Exception {
        Etunnel.Tunnel tunnel = Etunnel.connect("%(server)s", 3000, null);
        System.out.println("public URL: " + tunnel.url);

        Runtime.getRuntime().addShutdownHook(new Thread(tunnel::close));
        Thread.currentThread().join();   // keep the tunnel open
    }
}""",
        "cli": "java src/main/java/dev/ggcode/Etunnel.java 3000   # single-file launch, JDK 11+",
        "api": "| `Etunnel.connect(server, localPort, Options)` | `Tunnel` (blocking until `welcome`) |\n| `Tunnel.url` / `.hostname` / `.subdomain` / `.plan` | connection facts |\n| `Tunnel.close()` | graceful shutdown |",
        "opts": "`new Etunnel.Options().token(...).subdomain(...)` - or pass `null` for an anonymous tunnel.",
        "notes": "- Buffer-based client (no WS upgrades); the [Go SDK](../../sdk) handles those.\n- One JVM thread per tunnel plus a small executor for stream handling; no global state.\n- Behind a corporate proxy, `java.net.http` honours `-Dhttps.proxyHost` / `-Dhttps.proxyPort`.",
    },
    {
        "dir": "kotlin",
        "name": "Kotlin",
        "badges": f"[![Maven Central](https://img.shields.io/maven-central/v/dev.ggcode/etunnel?label=Maven%20Central)](https://central.sonatype.com/artifact/dev.ggcode/etunnel)\n[![JDK](https://img.shields.io/badge/JDK-11%2B-orange)](https://central.sonatype.com/artifact/dev.ggcode/etunnel)",
        "blurb": "Kotlin port of the Java SDK: same artifact, default arguments instead of an Options object.",
        "lang": "kotlin",
        "install": f"""\
implementation "dev.ggcode:etunnel:{VERSION}"   // same artifact as Java""",
        "embed": """\
fun main() {
    val tunnel = Etunnel.connect("%(server)s", 3000)
    println("public URL: " + tunnel.url)

    Runtime.getRuntime().addShutdownHook(Thread { tunnel.close() })
    Thread.currentThread().join()
}""",
        "cli": "kotlinc Etunnel.kt -include-runtime -d etunnel.jar\njava -jar etunnel.jar 3000",
        "api": "| `Etunnel.connect(server, localPort, token = null, subdomain = null)` | `Etunnel` (blocking) |\n| `Etunnel.url` | public HTTPS URL |\n| `Etunnel.close()` | graceful shutdown |",
        "opts": "Named arguments: `Etunnel.connect(server, 3000, token = \"et_...\", subdomain = \"myapp\")`",
        "notes": "- Buffer-based client (no WS upgrades); the [Go SDK](../../sdk) handles those.\n- Uses the JDK WebSocket client, so it runs on any JDK 11+ without extra dependencies.",
    },
    {
        "dir": "scala",
        "name": "Scala",
        "badges": f"[![Maven Central](https://img.shields.io/maven-central/v/dev.ggcode/etunnel?label=Maven%20Central)](https://central.sonatype.com/artifact/dev.ggcode/etunnel)\n[![JDK](https://img.shields.io/badge/JDK-11%2B-orange)](https://central.sonatype.com/artifact/dev.ggcode/etunnel)",
        "blurb": "Scala 2/3 friendly wrapper around the JDK WebSocket client; `connect` returns the public URL as a `String`.",
        "lang": "scala",
        "install": f"""\
libraryDependencies += "dev.ggcode" %% "etunnel" % "{VERSION}\"""",
        "embed": """\
object Main {
  def main(args: Array[String]): Unit = {
    val url = Etunnel.connect("%(server)s", 3000)
    println("public URL: " + url)
    Thread.currentThread().join()
  }
}""",
        "cli": "scala Etunnel.scala 3000",
        "api": "| `Etunnel.connect(server, localPort, token = null, subdomain = null)` | `String` - the public URL |\n| `Etunnel.close()` | graceful shutdown |",
        "opts": "Default arguments keep call sites short: `Etunnel.connect(server, 3000, subdomain = \"myapp\")`",
        "notes": "- Buffer-based client (no WS upgrades); the [Go SDK](../../sdk) handles those.\n- No macros, no implicits: compiles on Scala 2.13 and 3.x.",
    },
    {
        "dir": "csharp",
        "name": "C#",
        "badges": "[![NuGet](https://img.shields.io/nuget/v/etunnel?label=NuGet%20etunnel)](https://www.nuget.org/packages/etunnel)\n[![.NET](https://img.shields.io/badge/.NET-8%2B-512bd4)](https://www.nuget.org/packages/etunnel)",
        "blurb": "Single-file .NET client over `ClientWebSocket`: no NuGet dependencies, no async plumbing to write.",
        "lang": "csharp",
        "install": """\
# single file: copy Etunnel.cs into your project (the Etunnel class is public)
curl -O https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/csharp/Etunnel.cs

# or as a file-based app / CLI tool
dotnet add package etunnel""",
        "embed": """\
var tunnel = await Etunnel.Connect("%(server)s", "3000");
Console.WriteLine("public URL: " + tunnel.Url);

Console.CancelKeyPress += async (_, e) => { e.Cancel = true; await tunnel.Close(); };
await Task.Delay(Timeout.Infinite);   // keep the tunnel open""",
        "cli": "dotnet run Etunnel.cs 3000",
        "api": "| `Etunnel.Connect(server, local, token: null, subdomain: null)` | `Task<Etunnel.Tunnel>` |\n| `Tunnel.Url` | public HTTPS URL |\n| `Tunnel.Close()` | graceful shutdown |",
        "opts": "Named arguments: `Etunnel.Connect(server, \"3000\", token: \"et_...\", subdomain: \"myapp\")`",
        "notes": "- Buffer-based client (no WS upgrades); the [Go SDK](../../sdk) handles those.\n- `Etunnel.cs` contains top-level statements for the CLI; drop the class into your project and delete those lines when embedding.",
    },
    {
        "dir": "rust",
        "name": "Rust",
        "badges": "[![crates.io](https://img.shields.io/crates/v/etunnel?label=crates.io%20etunnel)](https://crates.io/crates/etunnel)\n[![Rust](https://img.shields.io/badge/rust-edition%202021-dea584)](https://crates.io/crates/etunnel)",
        "blurb": "Small binary crate (tungstenite + ureq) you install and run beside your service.",
        "lang": "sh",
        "install": "cargo install etunnel",
        "embed": """\
# the crate ships a binary target, so drive it as a child process:
use std::process::Command;

let mut child = Command::new("etunnel")
    .arg("3000")
    .env("ETUNNEL_SERVER", "%(server)s")
    .spawn()?;

// ... run your service ...

child.kill().ok();""",
        "cli": "etunnel 3000\nETUNNEL_SERVER=%(server)s etunnel 3000",
        "api": "| `etunnel [local]` | prints `TUNNEL_URL: https://...` and serves until killed |\n| env | `ETUNNEL_SERVER`, `ETUNNEL_TOKEN`, `ETUNNEL_SUBDOMAIN` |",
        "opts": "Positional local port or `host:port`; everything else comes from the environment.",
        "notes": "- Buffer-based client (no WS upgrades); the [Go SDK](../../sdk) handles those.\n- No `lib` target yet - `cargo add etunnel` gives you the binary's code, not a library API. Track the issue if you need an embeddable crate.",
    },
    {
        "dir": "swift",
        "name": "Swift",
        "badges": "[![Swift](https://img.shields.io/badge/swift-5.9%2B-F05138?logo=swift)](https://www.swift.org)\n[![SPM](https://img.shields.io/badge/SPM-compatible-brightgreen)](../../Package.swift)",
        "blurb": "Foundation-only async client built on `URLSessionWebSocketTask`; a single file, no package dependencies.",
        "lang": "swift",
        "install": """\
# single file (Foundation only): copy it into your target
curl -O https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/swift/etunnel.swift

# or build the bundled executable target
swift build -c release""",
        "embed": """\
import Foundation

let tunnel = Etunnel(server: "%(server)s", local: "3000")
let url = try await tunnel.connect()
print("public URL: " + url)""",
        "cli": "swift run etunnel 3000\n# or: swift build -c release && .build/release/etunnel 3000",
        "api": "| `Etunnel(server:local:)` | creates the client (no I/O yet) |\n| `await connect(token:subdomain:)` | `String` - the public URL, after `welcome` |\n| `close()` | graceful shutdown |",
        "opts": "Both arguments are optional: `try await tunnel.connect(token: \"et_...\", subdomain: \"myapp\")`",
        "notes": "- Buffer-based client (no WS upgrades); the [Go SDK](../../sdk) handles those.\n- `etunnel.swift` is the entire client and contains no top-level code, so it can be vendored into an app target as is; `main.swift` is only the CLI entry point.",
    },
    {
        "dir": "dart",
        "name": "Dart",
        "badges": "[![pub.dev](https://img.shields.io/pub/v/etunnel?label=pub.dev%20etunnel)](https://pub.dev/packages/etunnel)\n[![Dart](https://img.shields.io/badge/dart-3%2B-0175C2?logo=dart)](https://pub.dev/packages/etunnel)",
        "blurb": "`dart:io` `WebSocket` client for servers, CLIs and Flutter tooling - no pub dependencies.",
        "lang": "dart",
        "install": "dart pub add etunnel",
        "embed": """\
import 'dart:io';
import 'package:etunnel/etunnel.dart';

Future<void> main() async {
  final tunnel = await Etunnel.connect('%(server)s', '3000');
  print('public URL: ' + tunnel.url);

  ProcessSignal.sigint.watch().listen((_) => exit(0));
}""",
        "cli": "dart run etunnel.dart 3000",
        "api": "| `Etunnel.connect(server, local, {token, subdomain})` | `Future<Etunnel>` |\n| `Etunnel.url` | public HTTPS URL |\n| `Etunnel.close()` | graceful shutdown |",
        "opts": "Named arguments: `Etunnel.connect(server, '3000', subdomain: 'myapp')`",
        "notes": "- Buffer-based client (no WS upgrades); the [Go SDK](../../sdk) handles those.\n- Runs on the Dart VM (servers/CLIs). Not for Flutter web, where `dart:io` sockets are unavailable.",
    },
    {
        "dir": "ruby",
        "name": "Ruby",
        "badges": "[![Gem](https://img.shields.io/gem/v/etunnel?label=gem%20etunnel)](https://rubygems.org/gems/etunnel)\n[![Ruby](https://img.shields.io/badge/ruby-2.7%2B-CC342D?logo=ruby)](https://rubygems.org/gems/etunnel)",
        "blurb": "Gem built on faye-websocket: one `connect` call, then the tunnel serves requests while your process runs.",
        "lang": "ruby",
        "install": "gem install etunnel faye-websocket",
        "embed": """\
require 'etunnel'

tunnel = Etunnel.connect('%(server)s', 3000)
puts "public URL: #{tunnel.url}"

trap('INT') { exit }
sleep""",
        "cli": "etunnel 3000",
        "api": "| `Etunnel.connect(server, local, token: nil, subdomain: nil)` | tunnel object |\n| `tunnel.url` | public HTTPS URL |\n| `tunnel.close` | graceful shutdown |",
        "opts": "Keyword arguments: `Etunnel.connect(server, 3000, subdomain: 'myapp', token: 'et_...')`",
        "notes": "- Buffer-based client (no WS upgrades); the [Go SDK](../../sdk) handles those.\n- Starts an EventMachine reactor in a background thread if none is running.\n- Rails: call `connect` from an initializer so the tunnel survives request reloads.",
    },
    {
        "dir": "php",
        "name": "PHP",
        "badges": "[![Packagist](https://img.shields.io/packagist/v/ggcode/etunnel?label=Packagist%20ggcode%2Fetunnel)](https://packagist.org/packages/ggcode/etunnel)\n[![PHP](https://img.shields.io/badge/php-8.1%2B-777bb4?logo=php)](https://packagist.org/packages/ggcode/etunnel)",
        "blurb": "Hand-rolled RFC 6455 client over PHP streams: Composer-installable, no curl or ReactPHP dependency (OpenSSL only for `wss`).",
        "lang": "php",
        "install": "composer require ggcode/etunnel",
        "embed": """\
<?php
require __DIR__ . '/vendor/autoload.php';

$tunnel = Etunnel::connect('%(server)s', 3000);
echo "public URL: {$tunnel->url}\\n";

$tunnel->run();   // serves requests until the process exits""",
        "cli": "php etunnel.php 3000",
        "api": "| `Etunnel::connect(server, local, token = null, subdomain = null, onUrl = null)` | `Etunnel` |\n| `$tunnel->url` | public HTTPS URL |\n| `$tunnel->run()` | blocking serve loop |",
        "opts": "The optional fifth argument is a callback fired with the URL as soon as `welcome` arrives.",
        "notes": "- Buffer-based client (no WS upgrades); the [Go SDK](../../sdk) handles those.\n- PHP-CLI only: needs the `openssl` extension for `wss://`.",
    },
    {
        "dir": "perl",
        "name": "Perl",
        "badges": "[![Perl](https://img.shields.io/badge/perl-5.20%2B-39457E?logo=perl)](etunnel.pl)\n[![deps](https://img.shields.io/badge/deps-core%20JSON%3A%3APP-lightgrey)](etunnel.pl)",
        "blurb": "One self-contained script: the WebSocket handshake and framing are implemented directly on sockets, JSON::PP is the only dependency.",
        "lang": "perl",
        "install": """\
curl -O https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/perl/etunnel.pl
# core modules only - JSON::PP ships with Perl 5.14+""",
        "embed": """\
use strict;
use warnings;
require './etunnel.pl';   # defines etunnel_connect()

my $tunnel = etunnel_connect('%(server)s', '127.0.0.1:3000', {});
print "public URL: $tunnel->{url}\\n";

sleep 3600;   # the tunnel closes when the process exits""",
        "cli": "perl etunnel.pl 3000",
        "api": "| `etunnel_connect(server, \"host:port\", {token, subdomain})` | hashref with `url` |\n| process lifetime | defines the tunnel lifetime |",
        "opts": "`etunnel_connect($server, '127.0.0.1:3000', { subdomain => 'myapp', token => 'et_...' })`",
        "notes": "- Buffer-based client (no WS upgrades); the [Go SDK](../../sdk) handles those.\n- The included `etunnel.pl` also runs standalone: `perl etunnel.pl 3000`.",
    },
    {
        "dir": "lua",
        "name": "Lua",
        "badges": "[![LuaRocks](https://img.shields.io/badge/LuaRocks-ggcode.etunnel-8b4bb8?logo=lua)](https://luarocks.org/modules/topcheer/ggcode.etunnel)\n[![Lua](https://img.shields.io/badge/lua-5.3%2B-2C2D72?logo=lua)](https://luarocks.org/modules/topcheer/ggcode.etunnel)",
        "blurb": "LuaRocks rock for Lua 5.3+ using lua-http (has client SNI, required for `wss` against an ACME server).",
        "lang": "lua",
        "install": "luarocks install ggcode.etunnel\n# pulls lua-http + cqueues",
        "embed": """\
-- etunnel.lua is a script: run it, or wrap its connect() in your own runner
local etunnel = dofile('etunnel.lua')   -- executes the CLI when run directly

-- for an in-process tunnel, copy the file and remove the CLI tail at the end,
-- then: local tunnel = connect(server, 3000)""",
        "cli": "lua etunnel.lua 3000\nlua $(luarocks show ggcode.etunnel | awk '/Location/{print $2}')/etunnel.lua 3000",
        "api": "| `connect(server, local_port, token, subdomain)` | table `{url, run()}` (module-local function) |\n| `tunnel.run()` | blocking serve loop |",
        "opts": "`connect('wss://t.etun.uk/tunnel', 3000, 'et_...', 'myapp')`",
        "notes": "- Buffer-based client (no WS upgrades); the [Go SDK](../../sdk) handles those.\n- Requests are served sequentially (one at a time) - fine for webhooks and dev, not for concurrency-heavy APIs.\n- LuaSec has no client-side SNI, which is why this uses lua-http instead.",
    },
    {
        "dir": "clojure",
        "name": "Clojure",
        "badges": "[![Clojars](https://img.shields.io/badge/Clojars-dev.ggcode%2Fetunnel-clj-5881d8?logo=clojure)](https://clojars.org/dev.ggcode/etunnel-clj)\n[![JDK](https://img.shields.io/badge/JDK-11%2B-orange)](https://clojars.org/dev.ggcode/etunnel-clj)",
        "blurb": "deps.edn library around the JDK WebSocket client; `connect!` returns a deref-able state map with `:url`.",
        "lang": "clojure",
        "install": f"""\
;; deps.edn
{{:deps {{dev.ggcode/etunnel-clj {{:mvn/version "{VERSION}"}}
         cheshire/cheshire         {{:mvn/version "5.13.0"}}}}}}""",
        "embed": """\
(ns app.core
  (:require [etunnel :as et]))

(defn -main [& _]
  (let [tunnel (et/connect! "%(server)s" 3000)]
    (println "public URL:" (:url @tunnel))
    @(promise)))   ;; block: the tunnel closes with the JVM""",
        "cli": "clojure -M -m etunnel 3000",
        "api": "| `(connect! server local & {:keys [token subdomain]})` | atom holding `{:url :ws :close!}` |\n| `(:url @tunnel)` | public HTTPS URL once `welcome` arrives |\n| `(close! @tunnel)` | graceful shutdown |",
        "opts": "`(et/connect! server 3000 :subdomain \"myapp\" :token \"et_...\")`",
        "notes": "- Buffer-based client (no WS upgrades); the [Go SDK](../../sdk) handles those.\n- Small integers parse as `Integer` and frame ids as `Long` in cheshire; the reader normalises keys, so `:url` is always there.",
    },
    {
        "dir": "elixir",
        "name": "Elixir",
        "badges": "[![Hex](https://img.shields.io/hexpm/v/etunnel?label=Hex%20etunnel)](https://hex.pm/packages/etunnel)\n[![Elixir](https://img.shields.io/badge/elixir-1.14%2B-4B275F?logo=elixir)](https://hex.pm/packages/etunnel)",
        "blurb": "OTP-friendly client: a supervised process holds the tunnel and `Etunnel.url/1` reports the public URL.",
        "lang": "elixir",
        "install": f"""\
# mix.exs
defp deps do
  [{{:etunnel, "~> {VERSION.rsplit('.', 1)[0]}.0"}}]
end""",
        "embed": """\
# supervised (recommended)
children = [{{Etunnel, server: "%(server)s", local: 3000}}]
{{:ok, _}} = Supervisor.start_link(children, strategy: :one_for_one)

# or standalone
{{:ok, tunnel}} = Etunnel.start_link(server: "%(server)s", local: 3000)
IO.puts("public URL: " <> Etunnel.url(tunnel))
Process.sleep(:infinity)""",
        "cli": "mix run --no-halt runner.exs 3000",
        "api": "| `Etunnel.start_link(server:, local:, token:, subdomain:)` | `{:ok, pid}` |\n| `Etunnel.url(pid)` | `String` - public HTTPS URL |\n| `:local` | port number or `\"host:port\"` |",
        "opts": "`Etunnel.start_link(server: server, local: 3000, subdomain: \"myapp\", token: \"et_...\")`",
        "notes": "- Buffer-based client (no WS upgrades); the [Go SDK](../../sdk) handles those.\n- Uses `:gun` for the WebSocket connection, so the tunnel is an ordinary OTP child that restarts on crash.",
    },
    {
        "dir": "c",
        "name": "C",
        "badges": "[![C11](https://img.shields.io/badge/C-C11-00599C?logo=c)](etunnel.c)\n[![deps](https://img.shields.io/badge/deps-libwebsockets%20%2B%20libcurl%20%2B%20OpenSSL-lightgrey)](Makefile)",
        "blurb": "Single translation unit on libwebsockets + libcurl: `make` and you have a static-friendly client binary.",
        "lang": "sh",
        "install": "make && sudo make install   # needs libwebsockets, libcurl and OpenSSL headers",
        "embed": """\
# the client is one program: build it, then run it beside your service
cc -O2 -o etunnel etunnel.c -lwebsockets -lcurl -lssl -lcrypto
ETUNNEL_SERVER='%(server)s' ./etunnel 3000""",
        "cli": "./etunnel 3000\nETUNNEL_SERVER=%(server)s ETUNNEL_TOKEN=et_... ./etunnel 3000",
        "api": "| `./etunnel [local]` | prints `TUNNEL_URL: https://...`, serves until killed |\n| env | `ETUNNEL_SERVER`, `ETUNNEL_TOKEN`, `ETUNNEL_SUBDOMAIN` |",
        "opts": "Positional local port or `host:port`; the rest comes from the environment.",
        "notes": "- Buffer-based client (no WS upgrades); the [Go SDK](../../sdk) handles those.\n- No library target: embed by spawning the binary from your service (`fork`/`exec`, or a systemd unit next to your app).",
    },
    {
        "dir": "cpp",
        "name": "C++",
        "badges": "[![C++17](https://img.shields.io/badge/C%2B%2B-17-00599C?logo=cplusplus)](etunnel.cpp)\n[![deps](https://img.shields.io/badge/deps-websocketpp%20%2B%20OpenSSL-lightgrey)](CMakeLists.txt)",
        "blurb": "websocketpp + asio client: one binary, CMake build, no runtime dependencies beyond OpenSSL.",
        "lang": "sh",
        "install": "cmake -B build && cmake --build build   # needs websocketpp headers and OpenSSL",
        "embed": """\
# build once, run it beside your service
cmake -B build && cmake --build build
ETUNNEL_SERVER='%(server)s' ./build/etunnel 3000""",
        "cli": "./build/etunnel 3000",
        "api": "| `./build/etunnel [local]` | prints `TUNNEL_URL: https://...`, serves until killed |\n| env | `ETUNNEL_SERVER`, `ETUNNEL_TOKEN`, `ETUNNEL_SUBDOMAIN` |",
        "opts": "Positional local port or `host:port`; the rest comes from the environment.",
        "notes": "- Buffer-based client (no WS upgrades); the [Go SDK](../../sdk) handles those.\n- Single `.cpp` file with a `serve()` loop, so you can link it into your own binary if you supply a `main`.",
    },
]


def render(d: dict) -> str:
    version_badge = d.get("badges", "")
    parts = [
        f"# etunnel for {d['name']}\n",
        version_badge + "\n" if version_badge else "",
        f"{d['blurb']}\n",
        "Every SDK speaks the same wire protocol against any etunnel server - "
        f"the default is `{DEFAULT_SERVER}`, and `ETUNNEL_SERVER` or the first "
        "argument of `connect()` points at your own deployment.\n",
        "```mermaid\n"
        "flowchart LR\n"
        '  V["visitor<br/>https://&lt;subdomain&gt;.<domain>"] -->|HTTPS| S["etunneld<br/>TLS + plan limits"]\n'
        f'  S -->|"wss /tunnel"| C["{d["name"]} client<br/>in your process"]\n'
        '  C -->|HTTP| A["localhost:3000"]\n'
        "```\n",
        "## Install\n",
        f"```{'sh' if d['lang'] in ('sh', 'js', 'ts', 'python', 'ruby', 'php', 'perl', 'lua', 'go', 'java', 'kotlin', 'scala', 'csharp', 'elixir', 'clojure', 'dart', 'swift') else ''}\n"
        f"{d['install']}\n```\n",
        "## Embed in your app\n",
        f"```{d['lang']}\n{d['embed'] % {'server': DEFAULT_SERVER}}\n```\n",
        "## CLI\n",
        f"```sh\n{d['cli'] % {'server': DEFAULT_SERVER} if '%(server)s' in d['cli'] else d['cli']}\n```\n",
        "Prints `TUNNEL_URL: https://<subdomain>.<domain>`; `Ctrl-C` closes the tunnel "
        "and the server frees the subdomain immediately.\n",
        "## API\n",
        "| Symbol | Returns / effect |\n|---|---|\n" + d["api"] + "\n",
        f"Options: {d['opts']}\n",
        "The tunnel keeps the URL for the lifetime of the process (or the plan TTL, "
        "whichever comes first); the server closes it with `bye` when the TTL expires, "
        "and `close()` releases it early.\n",
        "## Environment variables\n",
        ENV + "\n",
        "## Notes and limits\n",
        d["notes"] + "\n",
        "## Troubleshooting\n",
        TROUBLE + "\n",
        "## See also\n",
        f"- [All 20 SDKs](../README.md) - install matrix and capability comparison\n"
        f"- [Wire protocol](../../PROTOCOL.md) - frames, control messages, stream lifecycle\n"
        f"- [Repository]({REPO}) - server, Go SDK, examples, self-hosting\n"
        f"- [LICENSE](../../LICENSE) - MIT\n",
    ]
    return "\n".join(p for p in parts if p)


def main() -> None:
    written = []
    for d in L:
        path = ROOT / "sdks" / d["dir"] / "README.md"
        if not path.parent.is_dir():
            print(f"skip (no dir): {path}")
            continue
        path.write_text(render(d))
        written.append(str(path.relative_to(ROOT)))
    print(f"wrote {len(written)} READMEs")


if __name__ == "__main__":
    main()
