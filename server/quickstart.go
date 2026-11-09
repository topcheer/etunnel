package server

// Landing-page quickstart: one real, copy-pasteable integration per language.
//
// Each snippet is the smallest *embeddable* program for that ecosystem - the
// dependency line plus the code you would actually keep in your project. The
// CLI-only ports (C, C++, Rust) say so explicitly instead of pretending to
// have a library API.

import (
	"fmt"
	"html/template"
	"strings"
)

type quickstartSnippet struct {
	ID   string // anchor / css id, e.g. "python"
	Name string // tab label
	Mode string // "embed" or "cli"
	Dep  string // install command or dependency note
	File string // file name shown in the snippet header
	Docs string // docs path in the repo
	Code string
}

// quickstartSnippets returns the table rendered by the landing page. {{SERVER}}
// is replaced with the deployment's own endpoint so self-hosted pages show
// their own URL.
func quickstartSnippets() []quickstartSnippet {
	return []quickstartSnippet{
		{
			ID: "node", Name: "Node", Mode: "embed",
			Dep:  "npm install @easytunnel/et-sdk",
			File: "tunnel.js",
			Docs: "sdks/node",
			Code: `const { connect } = require('@easytunnel/et-sdk');

async function main() {
  // 3000 may also be '127.0.0.1:3000' or 'localhost:3000'
  const tunnel = await connect('{{SERVER}}', 3000);
  console.log('public URL:', tunnel.url);

  // the tunnel lives as long as this process (and the plan's TTL)
  process.on('SIGINT', () => { tunnel.close(); process.exit(0); });
}

main().catch((err) => { console.error(err); process.exit(1); });
`,
		},
		{
			ID: "typescript", Name: "TypeScript", Mode: "embed",
			Dep:  "npm install @easytunnel/et-sdk",
			File: "tunnel.ts",
			Docs: "sdks/typescript",
			Code: `import { connect } from '@easytunnel/et-sdk';

const tunnel = await connect('{{SERVER}}', 3000);

console.log('public URL:', tunnel.url);
console.log('expires on plan:', tunnel.plan);
`,
		},
		{
			ID: "deno", Name: "Deno", Mode: "embed",
			Dep:  "deno add jsr:@ggcode/etunnel",
			File: "tunnel.ts",
			Docs: "sdks/deno",
			Code: `import { connect } from 'jsr:@ggcode/etunnel';

const tunnel = await connect('{{SERVER}}', 3000);
console.log('public URL:', tunnel.url);

Deno.addSignalListener('SIGINT', () => {
  tunnel.close();
  Deno.exit(0);
});
`,
		},
		{
			ID: "python", Name: "Python", Mode: "embed",
			Dep:  "pip install etunnel",
			File: "tunnel.py",
			Docs: "sdks/python",
			Code: `from etunnel import connect

# port number or "host:port"; the tunnel runs on a background thread
tunnel = connect("{{SERVER}}", 3000)
print("public URL:", tunnel["url"])

try:
    input("press Enter to stop\n")
finally:
    tunnel["close"]()
`,
		},
		{
			ID: "go", Name: "Go", Mode: "embed",
			Dep:  "go get github.com/topcheer/etunnel/sdk",
			File: "main.go",
			Docs: "sdk",
			Code: `package main

import (
	"context"
	"log"

	"github.com/topcheer/etunnel/sdk"
)

func main() {
	tunnel, err := etunnel.Connect(context.Background(), etunnel.Options{
		Server:    "{{SERVER}}",
		LocalAddr: "127.0.0.1:3000",
	})
	if err != nil {
		log.Fatal(err)
	}
	defer tunnel.Close("app exiting")

	log.Printf("public URL: %s", tunnel.URL())

	// start your own HTTP server, or just block while the tunnel serves
	select {}
}
`,
		},
		{
			ID: "java", Name: "Java", Mode: "embed",
			Dep:  "dev.ggcode:etunnel (Maven Central, JDK 11+, no dependencies)",
			File: "Main.java",
			Docs: "sdks/java",
			Code: `import dev.ggcode.Etunnel;

public class Main {
    public static void main(String[] args) throws Exception {
        Etunnel.Tunnel tunnel = Etunnel.connect("{{SERVER}}", 3000, null);
        System.out.println("public URL: " + tunnel.url);

        Runtime.getRuntime().addShutdownHook(new Thread(tunnel::close));
        Thread.currentThread().join();   // keep the tunnel open
    }
}
`,
		},
		{
			ID: "kotlin", Name: "Kotlin", Mode: "embed",
			Dep:  "dev.ggcode:etunnel (Maven Central, JDK 11+)",
			File: "Main.kt",
			Docs: "sdks/kotlin",
			Code: `fun main() {
    val tunnel = Etunnel.connect("{{SERVER}}", 3000)
    println("public URL: " + tunnel.url)

    Runtime.getRuntime().addShutdownHook(Thread { tunnel.close() })
    Thread.currentThread().join()
}
`,
		},
		{
			ID: "scala", Name: "Scala", Mode: "embed",
			Dep:  "dev.ggcode:etunnel (Maven Central, JDK 11+)",
			File: "Main.scala",
			Docs: "sdks/scala",
			Code: `object Main {
  def main(args: Array[String]): Unit = {
    val url = Etunnel.connect("{{SERVER}}", 3000)
    println("public URL: " + url)
    Thread.currentThread().join()
  }
}
`,
		},
		{
			ID: "csharp", Name: "C#", Mode: "embed",
			Dep:  "single file: sdks/csharp/Etunnel.cs (.NET 8+, zero dependencies)",
			File: "Program.cs",
			Docs: "sdks/csharp",
			Code: `using System;

var tunnel = await Etunnel.Connect("{{SERVER}}", "3000");
Console.WriteLine("public URL: " + tunnel.Url);

Console.CancelKeyPress += async (_, e) => { e.Cancel = true; await tunnel.Close(); };
await Task.Delay(Timeout.Infinite);   // keep the tunnel open
`,
		},
		{
			ID: "swift", Name: "Swift", Mode: "embed",
			Dep:  "single file: sdks/swift/etunnel.swift (Foundation only)",
			File: "main.swift",
			Docs: "sdks/swift",
			Code: `import Foundation

let tunnel = Etunnel(server: "{{SERVER}}", local: "3000")
let url = try await tunnel.connect()
print("public URL: " + url)
`,
		},
		{
			ID: "dart", Name: "Dart", Mode: "embed",
			Dep:  "dart pub add etunnel",
			File: "bin/tunnel.dart",
			Docs: "sdks/dart",
			Code: `import 'package:etunnel/etunnel.dart';

Future<void> main() async {
  final tunnel = await Etunnel.connect('{{SERVER}}', '3000');
  print('public URL: ' + tunnel.url);

  ProcessSignal.sigint.watch().listen((_) => exit(0));
}
`,
		},
		{
			ID: "ruby", Name: "Ruby", Mode: "embed",
			Dep:  "gem install etunnel faye-websocket",
			File: "tunnel.rb",
			Docs: "sdks/ruby",
			Code: `require 'etunnel'

tunnel = Etunnel.connect('{{SERVER}}', 3000)
puts "public URL: #{tunnel.url}"

trap('INT') { exit }
sleep
`,
		},
		{
			ID: "php", Name: "PHP", Mode: "embed",
			Dep:  "composer require ggcode/etunnel",
			File: "tunnel.php",
			Docs: "sdks/php",
			Code: `<?php
require __DIR__ . '/vendor/autoload.php';

$tunnel = Etunnel::connect('{{SERVER}}', 3000);
echo "public URL: {$tunnel->url}\n";

$tunnel->run();   // serves requests until the process exits
`,
		},
		{
			ID: "perl", Name: "Perl", Mode: "embed",
			Dep:  "cpanm JSON::PP (single file: sdks/perl/etunnel.pl)",
			File: "tunnel.pl",
			Docs: "sdks/perl",
			Code: `use strict;
use warnings;
require './etunnel.pl';   # ships as one file, core modules only

my $tunnel = etunnel_connect('{{SERVER}}', '127.0.0.1:3000', {});
print "public URL: $tunnel->{url}\n";

sleep 3600;   # the tunnel closes when the process exits
`,
		},
		{
			ID: "lua", Name: "Lua", Mode: "cli",
			Dep:  "luarocks install ggcode.etunnel (LuaSec + lua-http)",
			File: "shell",
			Docs: "sdks/lua",
			Code: `luarocks install ggcode.etunnel
lua etunnel.lua 3000

# or as a script next to your app:
#   ETUNNEL_SERVER='{{SERVER}}' lua /path/to/etunnel.lua 3000
`,
		},
		{
			ID: "clojure", Name: "Clojure", Mode: "embed",
			Dep:  "dev.ggcode/etunnel-clj {:mvn/version \"1.2.4\"} + cheshire",
			File: "src/app/core.clj",
			Docs: "sdks/clojure",
			Code: `(ns app.core
  (:require [etunnel :as et]))

(defn -main [& _]
  (let [tunnel (et/connect! "{{SERVER}}" 3000)]
    (println "public URL:" (:url @tunnel))
    @(promise)))   ;; block: the tunnel closes with the JVM
`,
		},
		{
			ID: "elixir", Name: "Elixir", Mode: "embed",
			Dep:  "{:etunnel, \"~> 1.2\"}",
			File: "lib/app/application.ex",
			Docs: "sdks/elixir",
			Code: `defmodule App.Application do
  use Application

  def start(_type, _args) do
    children = [
      {Etunnel, server: "{{SERVER}}", local: 3000}
    ]

    Supervisor.start_link(children, strategy: :one_for_one)
  end
end

# or, outside a supervision tree:
# {:ok, tunnel} = Etunnel.start_link(server: "{{SERVER}}", local: 3000)
# IO.puts("public URL: " <> Etunnel.url(tunnel))
`,
		},
		{
			ID: "rust", Name: "Rust", Mode: "cli",
			Dep:  "cargo install etunnel",
			File: "shell",
			Docs: "sdks/rust",
			Code: `# the crate ships a binary, not a library: run it next to your service
cargo install etunnel
etunnel 3000

# or drive it as a child process from your own binary:
#   Command::new("etunnel").arg("3000").spawn()
`,
		},
		{
			ID: "c", Name: "C", Mode: "cli",
			Dep:  "single file: sdks/c/etunnel.c (libwebsockets + libcurl + OpenSSL)",
			File: "shell",
			Docs: "sdks/c",
			Code: `# build once, run beside your service
cc -O2 -o etunnel etunnel.c -lwebsockets -lcurl -lssl -lcrypto
ETUNNEL_SERVER='{{SERVER}}' ./etunnel 3000

# prints: TUNNEL_URL: https://<subdomain>.<domain>
`,
		},
		{
			ID: "cpp", Name: "C++", Mode: "cli",
			Dep:  "single file: sdks/cpp/etunnel.cpp (websocketpp + OpenSSL)",
			File: "shell",
			Docs: "sdks/cpp",
			Code: `c++ -std=c++17 -O2 -o etunnel etunnel.cpp -lssl -lcrypto
ETUNNEL_SERVER='{{SERVER}}' ./etunnel 3000
`,
		},
	}
}

// quickstartHTML renders the tab bar and one panel per language. Tabs are
// wired by showLang() in the landing page script.
func quickstartHTML(server string) (tabs, panels string) {
	var tb, pn strings.Builder
	for i, s := range quickstartSnippets() {
		on := ""
		if i == 0 {
			on = " on"
		}
		fmt.Fprintf(&tb, `<button class="qtab%s" data-id="%s" onclick="showLang(this)">%s</button>`, on, s.ID, s.Name)

		docs := "https://github.com/topcheer/etunnel/tree/main/" + s.Docs
		code := strings.ReplaceAll(s.Code, "{{SERVER}}", server)
		fmt.Fprintf(&pn, `<div class="panel%s" id="p-%s">
  <div class="snippet">
    <div class="snip-head">
      <span class="fname">%s</span>
      <span class="mode %s">%s</span>
      <span class="dep">%s</span>
      <a class="snip-link" href="%s">full docs</a>
      <button class="snip-copy" onclick="copySnippet(this)">copy</button>
    </div>
    <pre class="code">%s</pre>
  </div>
</div>`, on, s.ID, s.File, s.Mode, s.Mode, htmlEscapeText(s.Dep), docs, highlightCode(code))
	}
	return tb.String(), pn.String()
}

// Code keywords highlighted by highlightCode. Union across the embedded
// languages: the goal is readability, not a real lexer.
var codeKeywords = map[string]bool{
	"func": true, "fn": true, "def": true, "defn": true, "fun": true, "defmodule": true,
	"package": true, "import": true, "from": true, "require": true, "use": true, "using": true,
	"return": true, "if": true, "else": true, "elif": true, "for": true, "while": true, "loop": true,
	"let": true, "var": true, "const": true, "val": true, "static": true, "final": true,
	"class": true, "struct": true, "object": true, "module": true, "namespace": true,
	"public": true, "private": true, "internal": true, "override": true, "companion": true,
	"async": true, "await": true, "try": true, "catch": true, "except": true, "finally": true,
	"raise": true, "throw": true, "rescue": true, "begin": true, "end": true, "do": true,
	"then": true, "elsif": true, "unless": true, "when": true, "case": true, "match": true,
	"switch": true, "break": true, "continue": true, "defer": true, "go": true, "select": true,
	"new": true, "nil": true, "null": true, "None": true, "true": true, "false": true,
	"this": true, "self": true, "super": true, "type": true, "interface": true, "impl": true,
	"trait": true, "pub": true, "mut": true, "crate": true, "mod": true, "where": true,
	"println": true, "print": true, "puts": true, "echo": true, "printf": true,
	"System": true, "Console": true, "Runtime": true, "Math": true,
	"sleep": true, "exit": true, "main": true, "args": true,
	"string": true, "int": true, "bool": true, "void": true, "long": true,
	"uses": true, "and": true, "or": true, "not": true, "in": true, "with": true,
	"Logger": true, "log": true, "err": true,
}

func isIdentStart(c rune) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isIdentPart(c rune) bool {
	return isIdentStart(c) || c >= '0' && c <= '9' || c == '.' || c == '!' || c == '?' || c == '-'
}

func escCode(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(s)
}

// highlightCode emits spans for comments, strings and keywords. It is
// deliberately conservative: unknown constructs stay plain text.
func highlightCode(src string) template.HTML {
	var out strings.Builder
	rs := []rune(src)
	for i := 0; i < len(rs); i++ {
		c := rs[i]

		// comments: "#", "//", "--" (Lua)
		if c == '#' || (c == '/' && i+1 < len(rs) && rs[i+1] == '/') || (c == '-' && i+1 < len(rs) && rs[i+1] == '-') {
			j := i
			for j < len(rs) && rs[j] != '\n' {
				j++
			}
			out.WriteString(`<span class="cm">` + escCode(string(rs[i:j])) + `</span>`)
			i = j - 1
			continue
		}

		// strings: "..." '...'
		if c == '"' || c == '\'' {
			quote := c
			j := i + 1
			for j < len(rs) && rs[j] != quote {
				if rs[j] == '\\' {
					j++
				}
				j++
			}
			if j < len(rs) {
				j++
			}
			out.WriteString(`<span class="st">` + escCode(string(rs[i:j])) + `</span>`)
			i = j - 1
			continue
		}

		if isIdentStart(c) {
			j := i
			for j < len(rs) && isIdentPart(rs[j]) {
				j++
			}
			w := string(rs[i:j])
			if codeKeywords[w] {
				out.WriteString(`<span class="kw">` + escCode(w) + `</span>`)
			} else {
				out.WriteString(escCode(w))
			}
			i = j - 1
			continue
		}

		out.WriteString(escCode(string(c)))
	}
	return template.HTML(out.String())
}
