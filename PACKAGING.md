# Publishing the SDKs to official package registries

> **Counting note (20 vs 19):** the 20 language SDKs consist of the 19
> directories under `sdks/` **plus the canonical Go SDK at `/sdk`** — its
> manifest is the module-root `go.mod` and its README is `sdk/README.md`, so
> `ls sdks/*` counts only 19. Artifacts number 20 because the Clojure SDK
> ships jar+pom+deps.edn (3 files) as one SDK while Node ships 1 tgz: the
> artifact count and SDK count are independent.

All 20 SDKs have publish-ready manifests (`make pack-all` builds every artifact
locally into `dist/packages/`) and are published: the current release is
`v1.2.4` (`VERSION`), live on every registry listed below. This document records
per registry what the publish needs (account / token / approvals) and the exact
command, so a release can be reproduced or handed over.

CI builds and publishes from `.github/workflows/sdk-packages.yml` on every tag.

**Prerequisite for everything**: a public GitHub repository (git tags drive the
Go proxy, SPM, Packagist and deno.land/x). Cut a release with
`python3 scripts/bump-release.py <version>` and push the tag; see
[Release flow](#release-flow).

| # | Language | Registry | Account needed | Auth | Publish command | One-time gotchas |
|---|----------|----------|----------------|------|-----------------|------------------|
| 1 | Go (server/sdk) | proxy.golang.org | none (public GitHub repo is enough) | - | `git tag v$(cat VERSION) && git push --tags`, then `go get github.com/topcheer/etunnel@v$(cat VERSION)` | module path in go.mod must match the repo URL |
| 2 | Node | npmjs.com | npm org `easytunnel` | OIDC trusted publishing (no token) | `cd sdks/node && npm publish --access public` (CI job `publish-npm`, env `npm`) | published as `@easytunnel/et-sdk`; provenance on; repo `topcheer/etunnel` must match package.json |
| 3 | TypeScript | npmjs.com | npm account | `NPM_TOKEN` | `cd sdks/typescript && npm publish --access public` | published as `etunnel-ts` |
| 4 | Deno | JSR | GitHub login for JSR | browser / OIDC in CI | `cd sdks/deno && deno publish` | scope is `@ggcode` (`deno.json`); deno.land/x mirrors the git tag |
| 5 | Python | pypi.org | PyPI account | none w/ Trusted Publishing (GitHub OIDC) | `cd sdks/python && python -m build && twine upload dist/*` (or enable trusted publisher + use CI job) | name availability check; sdist+wheel both built by `python -m build` |
| 6 | Java | Maven Central | Central Portal account + **GPG key** | user/token in ~/.m2 | `cd sdks/java && mvn deploy` (needs distributionManagement + signing) | sign with GPG; groupId `dev.ggcode` (verified via ggcode.dev); javadoc + sources jars are mandatory |
| 7 | Kotlin | Maven Central | same as Java | same | `gradle publish` (add maven-publish plugin) | same GPG/rules; alternative: GitHub Packages (token only, no GPG) |
| 8 | Scala | Maven Central | same as Java | same | `sbt publishSigned` (add sonatype plugin) | same as Java |
| 9 | C# | nuget.org | NuGet account | API key | `dotnet nuget push dist/packages/etunnel.<version>.nupkg -k <KEY> -s nuget.org` | package ID `etunnel` availability check |
| 10 | Rust | crates.io | GitHub login | `cargo login` | `cd sdks/rust && cargo publish` | nothing special; name checked automatically |
| 11 | Swift | SPM (git tag) / Swift Package Index | none for SPM | - | `git tag v<version> && git push --tags`; submit repo to swiftpackageindex.com | no central registry — the tag IS the release |
| 12 | Dart | pub.dev | Google account | OAuth (browser) | `cd sdks/dart && dart pub publish` | description/pubspec checks run automatically; `--dry-run` first |
| 13 | Ruby | rubygems.org | RubyGems account (MFA) | `gem push` (OTP) | `gem push dist/packages/etunnel-<version>.gem` | name availability check |
| 14 | PHP | packagist.org | GitHub login | API token | push git tag, submit repo once at packagist.org | vendor `ggcode/etunnel` — change vendor to your GitHub org; publishing = git tags, no upload |
| 15 | Perl | CPAN (PAUSE) | **PAUSE account** (email approval, ~1-2 days) | PAUSE upload page | `cd sdks/perl && make dist` then upload tar.gz at pause.perl.org | slowest onboarding; account must match author field |
| 16 | Clojure | clojars.org | Clojars account | `CLOJARS_USERNAME`/`CLOJARS_TOKEN` | `mvn deploy` with the clojars repo, or `clojure -X:deploy` | group `dev.ggcode`, artifact `etunnel-clj` (Clojars groups are unverified by design) |
| 17 | Elixir | hex.pm | Hex account | `mix hex.user auth` | `cd sdks/elixir && mix hex.publish` | auto-generates docs; needs local elixir (CI has it) |
| 18 | Lua | luarocks.org | GitHub login | API key | `cd sdks/lua && luarocks upload etunnel-<version>-1.rockspec --api-key=<KEY>` | rockspec `source.url`/`tag` must point at the public git tag |
| 19 | C | GitHub Releases (+ vcpkg/conan optional) | none for releases | GH token for release API | `gh release create v<version> dist/packages/etunnel-c-<version>.tar.gz` | no central C registry; Homebrew tap optional |
| 20 | C++ | GitHub Releases (+ conan optional) | none for releases | GH token | `gh release create v<version> dist/packages/etunnel-cpp-<version>.tar.gz` | build needs Boost <= 1.86 or standalone asio <= 1.14 (websocketpp upstream is dormant); CI pins ubuntu-22.04 |

`<version>` below always means `cat VERSION` (currently `1.2.4`).

## Release flow

```sh
python3 scripts/bump-release.py 1.2.5      # stamps VERSION + every manifest
make verify && make pack-all               # compile check + local artifacts
git commit -am "release: v1.2.5" && git tag v1.2.5
git push origin main --tags                 # Go proxy, SPM, Packagist, deno.land/x go live
gh release create v1.2.5 dist/packages/*    # C/C++ artifacts
```

Tag-driven registries (Go proxy, SPM, Packagist, deno.land/x) need nothing else.
Token-driven registries publish from CI:

| Lane | Registries | Requirement |
|---|---|---|
| Fast, token only | npm (Node + TypeScript), PyPI (trusted publishing), crates.io, RubyGems, NuGet, Hex, LuaRocks, Clojars, pub.dev | repository secrets, already configured |
| Slow | Maven Central (Java/Kotlin/Scala) | GPG key + Central Portal account; approval can take hours |
| Paused | CPAN (Perl) | first upload needs a maintainer request |

## Naming

`etunnel` is short, so every registry was checked before the first publish:
npm/PyPI/RubyGems/NuGet/crates.io/pub.dev/Hex all carry `etunnel`, npm also
carries the scoped `@easytunnel/et-sdk`, and the JVM artifacts use the group
`dev.ggcode`. If a name ever becomes unavailable, only the `name` field in the
corresponding manifest changes (`@<org>/etunnel`, `ggcode-etunnel`, ...).

## Tag discipline (Packagist et al)

Tags of released versions are never rewritten. Re-tagging triggers Packagist
webhook re-syncs (supply-chain alert emails) and re-runs the whole publish
matrix. Fix-forward: land the fix, cut the NEXT tag. All 8 CI publish jobs
are idempotent (registry presence check / --skip-duplicate / skip-existing),
so a re-run of the same tag is safe but noisy; a rewritten tag is not.
