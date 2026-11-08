#!/usr/bin/env python3
"""Polish registry-facing package descriptions across all SDK manifests.

One unified value proposition, adapted to each registry's field and length:
instant public HTTPS for local services, zero signup, automatic TLS, works
behind NAT/firewalls, 20-language SDK family, single-binary server.
"""
import json, pathlib, re

ROOT = pathlib.Path(__file__).resolve().parent.parent
SHORT = ("Expose local services to the internet in one line of code: instant "
         "public HTTPS URLs with automatic TLS, through NAT and firewalls, no signup.")
LONG = (SHORT + " Webhook-friendly ngrok-style reverse tunnel, with SDKs in 20 "
        "languages and a single-binary Go server.")
KEYS = ["tunnel", "reverse-proxy", "webhook", "ngrok", "localhost", "expose", "easytunnel"]

def sub(p, pat, rep):
    p = pathlib.Path(p); s = p.read_text(); s2, n = re.subn(pat, rep, s, count=1, flags=re.M)
    assert n == 1, f"no match in {p}: {pat}"
    p.write_text(s2); print(f"  {p.name}: ok")

# npm (node)
p = ROOT/"sdks/node/package.json"; d = json.loads(p.read_text())
d["description"] = LONG; d["keywords"] = KEYS
p.write_text(json.dumps(d, indent=2) + "\n"); print("  node: ok")
# npm (typescript)
p = ROOT/"sdks/typescript/package.json"; d = json.loads(p.read_text())
d["description"] = LONG.replace("20 languages", "20 languages") + " Same wire protocol as @easytunnel/et-sdk."
d["keywords"] = KEYS + ["typescript"]
p.write_text(json.dumps(d, indent=2) + "\n"); print("  typescript: ok")
# rust
sub("sdks/rust/Cargo.toml", r'^description = ".*"', f'description = "{LONG}"')
# python
sub("sdks/python/pyproject.toml", r'^description = ".*"', f'description = "{SHORT}"')
# java pom
sub("sdks/java/pom.xml", r'(<description>).*?(</description>)', r'\g<1>' + LONG + r'\g<2>')
# clojure pom
sub("sdks/clojure/pom.xml", r'(<description>).*?(</description>)', r'\g<1>' + LONG + r'\g<2>')
# csharp
sub("sdks/csharp/Etunnel.csproj", r'(<Description>).*?(</Description>)', r'\g<1>' + LONG + r'\g<2>')
# ruby gemspec
sub("sdks/ruby/etunnel.gemspec", r"s\.summary = '.*'", f"s.summary = '{SHORT}'")
sub("sdks/ruby/etunnel.gemspec", r"s\.description = '.*?'\n                  '.*?'",
    "s.description = '#{LONG} '\n                  'Buffer-based protocol client using faye-websocket.'")
# php composer
sub("sdks/php/composer.json", r'("description": ").*?(")', r'\g<1>' + LONG + r'\g<2>')
# perl
sub("sdks/perl/Makefile.PL", r"ABSTRACT     => '.*'", f"ABSTRACT     => '{SHORT}'")
# elixir mix.exs
sub("sdks/elixir/mix.exs", r'(?s)(defp description do\n    ").*?(")',
    r'\g<1>' + LONG + r'\g<2>')
# dart pubspec
sub("sdks/dart/pubspec.yaml", r'^description: .*$', f'description: {SHORT}')
# deno.json has no description field; add after name
p = ROOT/"sdks/deno/deno.json"; d = json.loads(p.read_text())
d["description"] = LONG; p.write_text(json.dumps(d, indent=2) + "\n"); print("  deno: ok")
# lua rockspec
sub("sdks/lua/ggcode.etunnel-1.0.0-1.rockspec", r'summary = ".*"', f'summary = "{SHORT}"')
print("descriptions polished")
