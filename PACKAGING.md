# Publishing the SDKs to official package registries

> **Counting note (22 SDKs):** `ls -d sdks/*/` lists 22 directories, but one of
> them (`sdks/testtools/`) is the shared login-test fixture, not an SDK. That
> leaves 21 SDK directories (19 language ports plus the Android AAR and the
> HarmonyOS ArkTS package) **plus the canonical Go SDK at `/sdk`**, whose
> manifest is the module-root `go.mod` and whose README is `sdk/README.md`:
> **22 SDKs in 20 languages** (TypeScript ships as Node, TypeScript and Deno
> packages; Kotlin as a JVM source file and as the Android package). The
> artifact count is a different number again - `make pack-all` builds one
> tarball per SDK directory regardless of registry.

Status of the 22 SDKs (checked against the registries on 2026-11-24 with
`VERSION` = 1.7.35; keep this document version-free):

| # | SDK | Package | Where | How it is published | Status |
|---|-----|---------|-------|---------------------|--------|
| 1 | Go | `github.com/topcheer/etunnel/sdk` | Go module proxy | git tag only | published |
| 2 | Node | `@easytunnel/et-sdk` | npm | CI `publish-npm` | published (1.7.0-1.7.35 tarballs miss `route.js`, fix ships in the next release) |
| 3 | TypeScript | `etunnel-ts` (declared, never published) | - | - | **not published**: no CI job, npm answers 404. Use the Node package or copy `etunnel.ts` |
| 4 | Deno | `@ggcode/etunnel` | JSR | CI `publish-jsr` | published |
| 5 | Python | `etunnel` | PyPI | CI `publish-pypi` | published |
| 6 | Java | `dev.ggcode:etunnel` | Maven Central | CI `publish-central` | published |
| 7 | Kotlin | `dev.ggcode:etunnel-kotlin` | Maven Central | CI `publish-central-kotlin-scala` (3-way matrix) | first release pending (tag): jar without the runtime + sources + javadoc + pom, all GPG-signed; compiled by `scripts/build-central-kotlin-scala.sh` (kotlinc, stdlib 2.1.0, `-language-version 2.1`); source declares `package dev.ggcode.etunnel` |
| 8 | Scala | `dev.ggcode:etunnel_2.13`, `dev.ggcode:etunnel_3` | Maven Central | CI `publish-central-kotlin-scala` | first release pending (tag): cross-built by the same script with scalac 2.13.14 (the build job's Scala 2.13) and 3.3.9 LTS (TASTy readable by every 3.3+); source declares `package dev.ggcode.etunnel` |
| 9 | C# | `etunnel` | nuget.org | CI `publish-nuget` | published (package targets `net10.0` only) |
| 10 | Rust | `etunnel` | crates.io | CI `publish-crates` | published (binary only - the crate has no library target) |
| 11 | Swift | `etunnel` | SwiftPM (git tag) | git tag only | published (`Package.swift` at the repo root, sources in `sdks/swift`) |
| 12 | Dart | `etunnel` | pub.dev | CI `publish-dart` | published (1.7.35 ships `etunnel.dart` at the package root, not `lib/` - not importable as a package yet) |
| 13 | Ruby | `etunnel` | rubygems.org | CI `publish-rubygems` | published |
| 14 | PHP | `ggcode/etunnel` | packagist.org | git tag only (Packagist reads the **root** `composer.json`) | published |
| 15 | Perl | `etunnel` | CPAN | - | **paused** (PAUSE upload on hold; `cpan etunnel` is 404). Install from source: `perl etunnel.pl 3000` |
| 16 | Clojure | `dev.ggcode/etunnel-clj` | Clojars | CI `publish-clojars` | published (the pom declares no dependency on `cheshire`; add it to your deps) |
| 17 | Elixir | `etunnel` | hex.pm | CI `publish-hex` | published |
| 18 | Lua | `ggcode.etunnel` | luarocks.org | CI `publish-luarocks` | published (the 1.7.35 rockspec points at git tag `v1.4.0`, so `luarocks install` currently fails; build from source meanwhile) |
| 19 | C | - | GitHub Releases | none automated | source only: the release page of v1.7.35 has no assets; build from the tag (`make etunnel`) |
| 20 | C++ | - | GitHub Releases | none automated | source only, same as C (`cmake -B build`) |
| 21 | Android | `dev.ggcode:etunnel-android` | Maven Central | CI `publish-android` | published (AAR) |
| 22 | HarmonyOS | `etunnel` | ohpm | CI `publish-ohpm` (HAR compiled by `make pack-harmonyos`, pushed over the registry's npm-protocol API) | **pending first publish**: pipeline merged; the package is not on ohpm until the maintainer runs `scripts/publish-ohpm.sh` with `OHPM_TOKEN`. Until then copy `Etunnel.ets` from source |

## CI: what runs on a tag

`.github/workflows/sdk-packages.yml` runs on every `v*` tag: one `build`
job (`go test`, `make pack-all`, `make verify`-style syntax gates, version
gate) followed by **13 publish jobs**. All of them are idempotent: each one
probes the registry first and only pushes on an explicit "not found" answer
(#411); any other probe failure fails the job instead of blind-publishing.

| CI job | Artifact | Auth | Account needed | Registry-side one-time setup |
|---|---|---|---|---|
| `publish-npm` | `@easytunnel/et-sdk` (`cd sdks/node && npm publish --access public`) | OIDC trusted publishing, no token; environment `npm` | npm org `easytunnel` | Packages -> Connect npm package -> trusted publisher `topcheer/etunnel` / `sdk-packages.yml` / env `npm` |
| `publish-pypi` | `etunnel` (`python -m build` + `pypa/gh-action-pypi-publish`) | OIDC trusted publishing; environment `pypi` | PyPI account | pending publisher: owner `topcheer`, repo `etunnel`, workflow `sdk-packages.yml`, env `pypi` |
| `publish-rubygems` | `etunnel` gem (`rubygems/release-gem`) | OIDC trusted publishing; environment `rubygems` | RubyGems account (MFA) | trusted publisher on the gem page |
| `publish-nuget` | `etunnel` (`dotnet pack` + `dotnet nuget push`; the package targets `net10.0` only) | OIDC -> temporary API key; environment `nuget` | nuget.org account | trusted publishing, Active, glob `*` |
| `publish-hex` | `etunnel` (`mix hex.publish`) | Hex workload identity (OIDC); environment `hex` | Hex account | workload identity for `topcheer/etunnel` / env `hex` (the very first release had to be pushed by hand) |
| `publish-dart` | `etunnel` (`dart pub publish --force`) | pub.dev automated publishing (OIDC); environment `pub` | Google account | automated publishing: `topcheer/etunnel` + `sdk-packages.yml` |
| `publish-jsr` | `@ggcode/etunnel` (`deno publish --allow-dirty --no-check`) | OIDC (`id-token: write`), scope `@ggcode` linked to GitHub | GitHub login for JSR | scope `ggcode` created on jsr.io |
| `publish-central` | `dev.ggcode:etunnel` (`mvn -B -Pcentral -f sdks/java/pom.xml deploy`) | Portal user/token secrets `MAVEN_USERNAME` / `MAVEN_PASSWORD` + GPG (`GPG_PRIVATE_KEY`, `GPG_PASSPHRASE`) | Central Portal account + GPG key | verified namespace `dev.ggcode`; sources + javadoc jars are mandatory |
| `publish-central-kotlin-scala` | `dev.ggcode:etunnel-kotlin`, `dev.ggcode:etunnel_2.13`, `dev.ggcode:etunnel_3` (one matrix leg each: `sh scripts/build-central-kotlin-scala.sh <kotlin\|scala-2.13\|scala-3>` then `mvn -B -Pcentral -f <pom> deploy`) | the same Portal + GPG secrets as `publish-central` | Central Portal account + GPG key | jars are built outside Maven (kotlinc / scalac 2.13.14 + 3.3.9 via SDKMAN) and attached by build-helper; the javadoc jar is a hand-written javadoc-layout summary (Kotlin/Scala have no javadoc tool); idempotent per coordinate via the `repo1` pom probe (#412) |
| `publish-crates` | `etunnel` (`cargo publish --allow-dirty`) | long-lived token `CARGO_REGISTRY_TOKEN` (crates.io has no OIDC) | crates.io account (GitHub login) | nothing special |
| `publish-luarocks` | `ggcode.etunnel` (`luarocks upload <rockspec>`) | API key `LUAROCKS_API_KEY` | luarocks.org account (GitHub login) | nothing special |
| `publish-clojars` | `dev.ggcode:etunnel-clj` (`jar cf` of `etunnel.clj` + `mvn deploy:deploy-file`) | `CLOJARS_USERNAME` / `CLOJARS_TOKEN` | Clojars account | group `dev.ggcode` (Clojars groups are unverified by design) |
| `publish-android` | `dev.ggcode:etunnel-android` AAR (vanniktech maven publish) | OIDC trusted publishing, environment `maven`, **plus** the same GPG secrets as `publish-central`; falls back to the Portal user/token | Central Portal account + GPG key | trusted publisher for namespace `dev.ggcode` / env `maven` |
| `publish-ohpm` | `etunnel` HAR (`make pack-harmonyos` = tsc-compiled ArkTS, hvigor layout; `scripts/publish-ohpm.sh`) | publish token secret `OHPM_TOKEN` (`Authorization: Bearer`); no OIDC | ohpm.openharmony.cn account (Huawei developer) | nothing special once the name `etunnel` is free (it is: `GET /ohpm/etunnel` = 404) |

Three SDKs publish with no CI job because the registry mirrors the git tag:
the Go module proxy (`go.mod` at the repo root), SwiftPM (`Package.swift` at
the repo root, sources in `sdks/swift`) and Packagist (`composer.json` at the
repo root). A pushed tag is the release for those three.

## Release flow

```sh
V=$(cat VERSION)
python3 scripts/bump-release.py "$V"       # stamps VERSION + every manifest
make verify && make pack-all               # compile check + local artifacts
git commit -am "release: v$V" && git tag "v$V"
git push origin main --tags                 # Go proxy, SPM, Packagist go live; CI runs the 12 publish jobs
gh release create "v$V" --title "v$V" --generate-notes   # optional notes page (no assets are attached)
```

`<version>` below always means `cat VERSION` (single source of truth).

### Known gaps in the stamping chain

Both README generators (`scripts/gen_sdk_readmes.py`, `scripts/gen-readmes.py`)
are retired: SDK READMEs are hand-written and `make verify` checks their
section structure.

`bump-release.py` stamps the manifests that are published. Not stamped, and
therefore drifting: `sdks/kotlin/build.gradle.kts` and `sdks/scala/build.sbt`
(both still 1.4.0, both unpublished), the `source.tag` line inside the Lua
rockspec (1.7.35's rockspec points at `v1.4.0`), and the duplicate
`sdks/php/composer.json` (`easytunnel/et-sdk` 1.4.0 - Packagist uses the root
`composer.json`, so this file is dead). Fix them at the source rather than in
the docs; until then the rows above say "not published" / "build from source".

## Naming

`etunnel` is short, so every registry was checked before the first publish:
npm/PyPI/RubyGems/NuGet/crates.io/pub.dev/Hex all carry `etunnel`, npm also
carries the scoped `@easytunnel/et-sdk`, JSR has `@ggcode/etunnel`, LuaRocks
has `ggcode.etunnel`, Packagist has `ggcode/etunnel`, and the JVM artifacts use
the group `dev.ggcode`. If a name ever becomes unavailable, only the `name`
field in the corresponding manifest changes (`@<org>/etunnel`,
`ggcode-etunnel`, ...).

## Tag discipline (Packagist et al)

Tags of released versions are never rewritten. Re-tagging triggers Packagist
webhook re-syncs (supply-chain alert emails) and re-runs the whole publish
matrix. Fix-forward: land the fix, cut the NEXT tag. All 12 CI publish jobs
are idempotent (registry presence check / --skip-duplicate / skip-existing),
so a re-run of the same tag is safe but noisy; a rewritten tag is not.
