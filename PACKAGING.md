# Publishing the SDKs to official package registries

> **Counting note (20 vs 19):** the 20 language SDKs consist of the 19
> directories under `sdks/` **plus the canonical Go SDK at `/sdk`** — its
> manifest is the module-root `go.mod` and its README is `sdk/README.md`, so
> `ls sdks/*` counts only 19. Artifacts number 20 because the Clojure SDK
> ships jar+pom+deps.edn (3 files) as one SDK while Node ships 1 tgz: the
> artifact count and SDK count are independent.

All 20 SDKs now have publish-ready manifests (`make pack-all` builds every
artifact locally into `dist/packages/`). This document lists, per registry,
what you still need (account / token / one-time approvals) and the exact
publish command. CI skeletons are in `.github/workflows/sdk-packages.yml`
(publish jobs are commented out until secrets exist).

**Prerequisite for everything**: push this repo to GitHub (it is not a git
repo yet) and tag `v1.0.0` — several registries (Go proxy, SPM, Packagist)
publish straight from git tags with no account at all.

| # | Language | Registry | Account needed | Auth | Publish command | One-time gotchas |
|---|----------|----------|----------------|------|-----------------|------------------|
| 1 | Go (server/sdk) | proxy.golang.org | none (GitHub repo is enough) | - | `git tag v1.0.0 && git push --tags` then `GOPROXY=proxy.golang.org go get etunnel@v1.0.0` | repo must be public; module path in go.mod must match repo URL |
| 2 | Node | npmjs.com | npm org `easytunnel` | OIDC trusted publishing (no token) | `cd sdks/node && npm publish --access public` (CI job `publish-npm`, env `npm`) | published as `@easytunnel/et-sdk`; provenance on; repo `topcheer/etunnel` must match package.json |
| 3 | TypeScript | npmjs.com | npm account | `NPM_TOKEN` | `cd sdks/typescript && npm publish --access public` | published as `etunnel-ts` |
| 4 | Deno | JSR / deno.land/x | GitHub login for JSR | browser | `cd sdks/deno && deno publish` | JSR requires a scope: change `deno.json` name to `@<yourscope>/etunnel`; deno.land/x needs the git tag |
| 5 | Python | pypi.org | PyPI account | none w/ Trusted Publishing (GitHub OIDC) | `cd sdks/python && python -m build && twine upload dist/*` (or enable trusted publisher + use CI job) | name availability check; sdist+wheel both built by `python -m build` |
| 6 | Java | Maven Central | Central Portal account + **GPG key** | user/token in ~/.m2 | `cd sdks/java && mvn deploy` (needs distributionManagement + signing) | must sign with GPG; groupId `com.ggcode` must match a domain you own (or use `io.github.<user>`); javadoc+sources jars required by Central |
| 7 | Kotlin | Maven Central | same as Java | same | `gradle publish` (add maven-publish plugin) | same GPG/rules; alternative: GitHub Packages (token only, no GPG) |
| 8 | Scala | Maven Central | same as Java | same | `sbt publishSigned` (add sonatype plugin) | same as Java |
| 9 | C# | nuget.org | NuGet account | API key | `dotnet nuget push dist/packages/etunnel.1.0.0.nupkg -k <KEY> -s nuget.org` | package ID `etunnel` availability check |
| 10 | Rust | crates.io | GitHub login | `cargo login` | `cd sdks/rust && cargo publish` | nothing special; name checked automatically |
| 11 | Swift | SPM (git tag) / Swift Package Index | none for SPM | - | `git tag v1.0.0 && git push --tags`; submit repo to swiftpackageindex.com | no central registry — the tag IS the release |
| 12 | Dart | pub.dev | Google account | OAuth (browser) | `cd sdks/dart && dart pub publish` | description/pubspec checks run automatically; `--dry-run` first |
| 13 | Ruby | rubygems.org | RubyGems account (MFA) | `gem push` (OTP) | `gem push dist/packages/etunnel-1.0.0.gem` | name availability check |
| 14 | PHP | packagist.org | GitHub login | API token | push git tag, submit repo once at packagist.org | vendor `ggcode/etunnel` — change vendor to your GitHub org; publishing = git tags, no upload |
| 15 | Perl | CPAN (PAUSE) | **PAUSE account** (email approval, ~1-2 days) | PAUSE upload page | `cd sdks/perl && make dist` then upload tar.gz at pause.perl.org | slowest onboarding; account must match author field |
| 16 | Clojure | clojars.org | Clojars account | `CLOJARS_USERNAME`/`CLOJARS_TOKEN` | `mvn deploy` with clojars repo, or `clojure -X:deploy` | group `com.ggcode` must be a verified group (your domain) or use ungrouped `etunnel` |
| 17 | Elixir | hex.pm | Hex account | `mix hex.user auth` | `cd sdks/elixir && mix hex.publish` | auto-generates docs; needs local elixir (CI has it) |
| 18 | Lua | luarocks.org | GitHub login | API key | `cd sdks/lua && luarocks upload etunnel-1.0.0-1.rockspec --api-key=<KEY>` | rockspec `source.url`/`tag` must point at the public git tag |
| 19 | C | GitHub Releases (+ vcpkg/conan optional) | none for releases | GH token for release API | `gh release create v1.0.0 dist/packages/etunnel-c-1.0.0.tar.gz` | no central C registry; Homebrew tap optional |
| 20 | C++ | GitHub Releases (+ conan optional) | none for releases | GH token | `gh release create v1.0.0 dist/packages/etunnel-cpp-1.0.0.tar.gz` | build needs Boost <= 1.86 or standalone asio <= 1.14 (websocketpp upstream is dormant); CI pins ubuntu-22.04 |

## Minimal path to "everything publishable"

1. `git init && git remote add origin git@github.com:<org>/etunnel.git && git push -u origin main`
2. `git tag v1.0.0 && git push --tags`  → Go proxy, SPM, deno.land/x, Packagist are LIVE
3. `gh release create v1.0.0 dist/packages/*` → C/C++ done
4. Token-based, no extra approval: npm (node+ts), PyPI (trusted publishing), crates.io, RubyGems, NuGet, Hex, LuaRocks, Clojars, pub.dev
5. Slow lanes: Maven Central (GPG + portal account) for Java/Kotlin/Scala; PAUSE for CPAN

## Naming caveat

`etunnel` is short and may be taken on npm/PyPI/RubyGems/NuGet. Before the
push step above, check each: `npm view etunnel`, `pip index versions etunnel`,
`gem search etunnel`, `nuget search etunnel`. If taken, the cheap fix is a
scoped/qualified name (`@<org>/etunnel`, `etunnel-tunnel`, `ggcode-etunnel`)
— only the `name` field in each manifest changes.
