# Changelog

## v1.7.42 — 2026-11-26

- release: hourly cadence (no code changes); SDKs report `hello.version` for the server-side client version gate (v1.7.40)

## v1.7.42 — 2026-11-26

- (fill in)

## v1.7.41 — 2026-11-26

- release: hourly cadence (no code changes); SDKs report `hello.version` for the server-side client version gate (v1.7.40)

## v1.7.41 — 2026-11-26

- (fill in)

## v1.7.40 — 2026-11-25

- **feat (protocol/server)**: client version gate. SDKs now report their own version in `hello.version`; etunneld (built with `-X github.com/topcheer/etunnel/server.Version`, `make build` / Dockerfile) refuses clients that are more than **5 patch releases behind** (or report no/invalid version) with error code `client_outdated` and a message carrying the upgrade URL (`-client-download-url`, default https://github.com/topcheer/etunnel). `-client-version-lag` (env `ETUNNEL_CLIENT_VERSION_LAG`, default 5, `-1` = off); builds without an injected version ("dev") keep the gate off. **Deploy the gated server only after an SDK release that sends `version` (this one) - older SDKs are refused otherwise** (PR #441)
- **feat (sdks)**: all 22 SDKs + the Go SDK/CLI send `hello.version` and surface `client_outdated` verbatim (message incl. the URL, non-zero exit). Found and fixed on the way: Java's hello had `version` appended outside the JSON object, Kotlin's WIP hello lacked its closing brace, Clojure had no error-frame branch (server text lost to a 15 s timeout), C/C++/HarmonyOS dropped server `error` frames silently, Lua/Rust surfaced only the code (PR #440, #442, #443, #444)
- **docs**: PROTOCOL.md hello field + error code + "Client version policy"; RUNBOOK deployment-order note

## v1.7.39 — 2026-11-25

- **ci**: `publish-central-kotlin-scala` fixed - SDKMAN's second `scala` install prompted for a default in CI and aborted the job (v1.7.38 shipped the Kotlin/Scala coordinates nowhere); `make pack-kotlin/pack-scala` skip the Maven Central set when the cross compilers are absent instead of failing the release pack. No SDK code changes - this release exists so `dev.ggcode:etunnel-kotlin`, `etunnel_2.13` and `etunnel_3` reach Maven Central (PR #436, #437)

## v1.7.38 — 2026-11-25

- **feat (sdks)**: Kotlin and Scala SDKs are now published to Maven Central - `dev.ggcode:etunnel-kotlin` (Kotlin 2.1, JVM 11), `dev.ggcode:etunnel_2.13` (Scala 2.13.14) and `dev.ggcode:etunnel_3` (Scala 3.3.9 LTS); both sources moved into `package dev.ggcode.etunnel` so packaged consumers can import them; CI job `publish-central-kotlin-scala` (GPG-signed jar + sources + javadoc). Verified through Gradle / Maven / sbt / scalac consumers against a live etunneld (PR #435)
- **feat (harmonyos)**: ohpm publish pipeline - DevEco-free HAR build (`make pack-harmonyos`, tsc) and CI job `publish-ohpm` over the registry's npm-protocol endpoint with the `OHPM_TOKEN` secret (PR #434); first publish pending a valid ohpm publish token
- **ci**: `make verify` gains kotlinc / scalac compile gates; `make pack-all` stages the Maven Central set under `dist/packages/maven/`

## v1.7.37 — 2026-11-25

- **fix (php, #430)**: `composer require` then `php vendor/bin/etunnel.php 3000` no longer exits silently (Composer's bin proxy is recognised); multi-target routing accepts `local = null` like every other SDK (`serve()` no longer TypeErrors on the first request). Regression tests: `tests/test_composer_bin.sh`, `tests/test_routes_null.sh` (PR #432, #433)
- **fix (typescript, #427)**: `tsc --strict` (TS 5.9, NodeNext) is clean for `sdks/typescript/etunnel.ts` itself - `frame()` takes `Uint8Array`, the ESM `import.meta` guard is used consistently, the manifest declares `"type": "module"`; `make verify` runs the strict check offline (PR #432)
- **ci**: `make verify` now also runs `php -l` (PR #432)

## v1.7.36 — 2026-11-25

- **fix (sdks, P0)**: npm `@easytunnel/et-sdk` 1.7.0-1.7.35 shipped without `route.js` (package `files` whitelist) - `require()` and the CLI crashed on every install; fixed + CI now smoke-tests the packed tarball before publishing (PR #419)
- **fix (sdks, P0)**: Python SDK sent an `Origin` header and was rejected with 403 by etunneld >= 1.7.29 ("browser-origin tunnels are not supported") - `suppress_origin=True`; `on_close` no longer fires twice on explicit close; `hello.client` carries the real version (PR #420)
- **fix (sdks, P0)**: Elixir `Etunnel.start_link/1` crashed with `KeyError :last_pong` on the first frame (missing state keys); Ruby CLI/embed hit `SystemStackError` in `tunnel.close` on Ruby 4 - tunnel is now a plain class (PR #420)
- **fix (sdks, P0)**: C SDK sent an `Origin` header and could not connect to any current server (403); Android AAR's `Etunnel` class was `internal` + default package, unusable from consumer apps - now `dev.ggcode.etunnel.Etunnel` (PR #416, #423)
- **fix (sdks, P1)**: Clojure `etunnel-clj` pom declared no dependencies (cheshire) and printed `DBG stored-ok` per request; TypeScript vendored file started the CLI supervisor on import; root `Package.swift` broke SPM consumers (`import etunnel`); Dart pub archive had no `lib/` (not importable) and percent-encoded request paths; Lua rockspec fetched a stale `v1.4.0` tag; Rust gained a library target (PR #421, #423)
- **fix (sdks, P2)**: Scala one-shot mode printed `Tunnel(<url>,<resumeToken>)` instead of `TUNNEL_URL: <url>`; harmonyos `oh-package.json5` is now version-stamped; cpp CMake hint names the real `ASIO_INCLUDE_DIR` (PR #423)
- **docs**: every SDK README, README/PROTOCOL/PACKAGING and examples were re-verified by running each command and snippet against a live etunneld; every README carries a copyable **AI agent quick-start** prompt; new `llms.txt`; both README generators retired in favour of a `make verify` structure gate (PR #415-#419, #422)

## v1.7.35 — 2026-11-24

- **fix (reliability)**: PG session ledger resilience - when Postgres is unreachable at startup the server now degrades to journal-only instead of exiting, with a 30s background reconnect loop; optional fail-closed mode via `-ledger-strict` / `ETUNNEL_LEDGER_STRICT`; runtime ledger ctx budget raised 3s -> 10s; pool tuned to Max 4 / Min 1 / HealthCheck 1m; `/stats` gains `ledger_mode`, `ledger_failed_total`, `ledger_reconnects` (PR #408). Refs #407 - the infrastructure decision stays open
- test: two-level barrier in `TestResumeConcurrentOverQuotaRejected` fixes the flaky overlap (#405) (PR #406)
- docs: ops deploy record - v1.7.34 (offline ip2region + geo labels) live on t.etun.uk (PR #404)

## v1.7.34 — 2026-11-23

- feat: offline ip2region geolocation for Telegram pushes - tunnel-open notifications, burst alerts and `/whois` now carry the visitor's region; new dependency `github.com/lionsoul2014/ip2region/binding/golang` (go.mod/go.sum tidied), flags `-geoip-db` / `-geoip-db6`, fully offline (no third-party lookup); README/privacy page updated and RUNBOOK §1b documents the setup steps (PR #402)
- docs: ops deploy record - v1.7.33 (/24 burst guard #399) live on t.etun.uk (#401)

## v1.7.33 — 2026-11-23

- **security / abuse-resistance**: /24 burst guard - new anonymous tunnels are now rate-limited per network prefix per minute via `-max-tunnels-per-ip24-min` (default 6), closing the subnet-rotation burst pattern seen in the field from 94.192.76.0/24; operators get Telegram `/bursts` (list currently throttled prefixes) and `/unthrottle` (lift a prefix's throttle) plus README/CLUSTER/systemd documentation (PR #399)

## v1.7.32 — 2026-11-23

- **security (HIGH)**: PayPal rollback attribution Cut - custom_id-based rollbacks now attribute to the right account (#394); PG tombstone ordering fixed (#395); kick release gate enforced (#396) (PR #397)
- fixes (rust): body budget semantics + ServeSlot guard (#382, #387) (PR #390)
- fixes (billing/session): capture-lookup GET, dispute outcome shapes, kick budget finisher + true-window regression (#380, #383, #384, #385) (PR #391)
- fixes (tg/admin/billing): tap buttons executable, one shared email validator, review-batch LOWs (#381, #386, #389) (PR #392)

Closes #394 #395 #396 #382 #387 #380 #383 #384 #385 #381 #386 #389

## v1.7.31 — 2026-11-23

- republish: version-manifest bump only, no code changes since v1.7.30 - re-anchors the ledger after v1.7.30's tag initially shipped without the deno/etunnel.ts + rust/Cargo.lock source stamps (fixed via #377), which made the sdk-packages version gate reject the first v1.7.30 tag and left its registry publish incomplete; this bump re-stamps every manifest (all four in-source client stamps included) and re-runs the full publish pipeline so registry artifacts land cleanly

## v1.7.30 — 2026-11-23

- **security**: account delete/downgrade/refund/ResetKey now cascade-kick the holder's online sessions (#350/#351/#363/#366) (PR #361, PR #372)
- **security**: Stripe dispute attribution fixed - partial refunds and merchant wins keep entitlements; PayPal/NOWPayments refund rollback shapes + expired rows removed (#352/#364/#365) (PR #372)
- sdk: Rust ureq 2 -> 3 rewrite (#212) (PR #344); oneshot mode served nothing - exited before any request (#345) (PR #349)
- sdk: Rust connect/recv timeouts, bounded serve threads, shared agent pool (#354/#357/#369) (PR #360, PR #371); scala/c/lua host:port (#358)
- telegram: parameter-less admin commands answer with tappable lists (#346) (PR #347); single tappable list + byte-budget truncation (#367), admin email normalization (#368) (PR #373)
- docs/config: NOWPayments configuration documented + startup price env validation (#355/#370) (PR #373); PROTOCOL/name comment drift sync (#359) (PR #362)
- ops: deploy records for v1.7.29 and the telegram tap commands (PR #343, PR #348)

## v1.7.29 — 2026-11-22

- **security**: server review batch - MaxTunnels TOCTOU, 101 header injection (CRLF), downgrade downlink timeouts, webauthn hardening, admin fail counters, PayPal dedup, Origin rejection, telegram cooldown hygiene (#321/#323) (PR #325)
- **security**: resume TOCTOU closed out - random-name resume joins the bounded acquire; O(1) counters (#331 follow-through) (PR #333)
- fixes: SDK semantic batch - half-close semantics, malformed-frame fatality, bye priority channel, proto gates (TS/Deno/Go) (#322) (PR #324)
- fixes: SDK review batch - upgrade leak reclaim on hung locals, TS/Deno proto gate, review LOWs (#330) (PR #332)
- site: cookieless analytics (analytics.ggcode.dev) on official pages (#326)
- site: privacy policy aligned with cookieless analytics; DNT honored (#327)
- docs: ops deploy records - #326 site analytics, #324/#325/#327 review fixes + DNT, #333/#332 review-fix batch (#328, #329, #334)
- ledger: this entry also clears the #331 bookkeeping debt - the six PRs above (#324/#325/#326/#327/#332/#333) are recorded here together instead of per-merge

## v1.7.28 — 2026-11-22

- **security**: resume path quota gate moves into the registry lock - the #312 check-then-consume TOCTOU is closed (#315) (PR #315)
- **security**: #295 debt batch - session revocation, refund rollback, admin cooldown, forged-header guard, reserved "me", loud activation errors (#295) (PR #313)
- fixes: second-review batch - quota atomicity, TTL timer gate, sameOrigin ports, Domain validation, copy-fail copy, loud truncation (#304) (PR #305)
- fixes: server backlog - #264 upgrade close asymmetry, #265 loud persistence errors, #268 post-hijack fallback, #269 docs sync, #240 money-path tests (#308)
- fixes: SDK backlog batch closing #243 / #263 / #266 / #267 across Go/Rust/Python/C#/JS-family/Elixir (#309)
- fixes: Go SDK declares the 502 page as text/html (was text/plain) (#311)
- features: telegram tunnel-open notification with per-subdomain cooldown, sent asynchronously (#316) (PR #316)
- tests: #240 money-path coverage - NOWPayments IPN, activate validation, short-domain lifecycle, PayPal credentials (#314)
- docs: ops deploy record for main@844a508 (#312 TOCTOU fix + telegram notify) live on t.etun.uk (#317)

## v1.7.27 — 2026-11-21

- **security**: resume tokens are now bound to the issuing identity, re-gated through auth on resume, and capped by TTL - a leaked or cross-account token can no longer resume someone else's tunnel (#292) (PR #296)
- **security**: /me actions and passkey removal now require same-origin CSRF protection - cross-site request forgeries are rejected (#293) (PR #297)
- **security**: upgrade responses meter their downlink and every proxied body write carries a per-write deadline - slow clients can no longer pin workers or bypass quota (#294) (PR #299)
- site: warm paper redesign - sunlit garden replaces GitHub dark (#290)
- site: one-click AI agent integration prompt block on the landing page (#298)
- tunnel: keep wire body identity end to end - declare what travels (#286) (PR #287)
- android sdk: replay threading, header fidelity, caps, cleartext and parity lows from review (#282) (PR #284)
- harmonyos sdk: welcome zombie socket, cancellable supervise, method passthrough from review (#283) (PR #285)
- test(android): raw-socket chunked-gzip replay identity anchor for the #286 blind spot (PR #289)
- ops: etunneld deploy runbook, deploy records for warm paper / agent block / the SEC batch, and the CSRF + resume-transition evidence archive (#288, #300, #301, #302)

## v1.7.26 — 2026-11-21

- sdks: republish Android AAR (1.7.25 upload stalled as a Portal draft; Central Portal automaticRelease now enabled) (#260)

## v1.7.25 — 2026-11-20

- sdks: HarmonyOS ArkTS SDK (@ohos.net.websocket, har packaging) (#260) (PR #273)

## v1.7.24 — 2026-11-20

- sdks: Android AAR SDK, Kotlin+OkHttp3 (#260) (PR #271)

## v1.7.23 — 2026-11-20

- sdks: cross-SDK stream caps and client ping (#243 M2/M3) (PR #262)
- docs: declare Swift SDK Apple platforms + planned Android/HarmonyOS (#260)
- docs(readme): annotate visionOS/Catalyst as untested in the Swift SDK platform note (#260) (PR #261)

## v1.7.22 — 2026-11-20

- auth: only trust provider-verified emails in the OAuth callback (#215) (PR #247)
- proxy: release the request-body pump when the stream finishes (#217) (PR #248)
- sdk: reset a stalled stream instead of parking the whole tunnel (#230) (PR #249)
- cluster: carry WebSocket upgrades through router Forward via hijacked pipes (#235) (PR #250)
- helm: fail the install when mesh/sticky lack SESSION_SECRET (#246) (PR #251)
- server: stop blocking Session.Close on cluster backends (#220) (PR #252)
- server: keep the per-IP tunnel limit enforced and un-lockable (#232) (PR #253)
- cluster: stop swallowing sticky routing-table errors (#236) (PR #254)
- ci: run the Go gate on every PR and push to main (#213) (PR #255)
- kotlin sdk: upgrade the Kotlin plugin to 2.2.0 and make the build real (#214) (PR #256)
- sdks (js/dart/python): handshake and frame robustness fixes (#243 M1/M4/M5) (PR #257)
- rust sdk: honest truncation, a real close(), dial error prefixes (#243 M7/M8/L1/L4) (PR #258)
- sdks: header fidelity, IPv6 locals, required-local guards (#243 M6/M9/L5/L7) (PR #259)
- chore: ignore kotlin gradle build outputs

## v1.7.21 — 2026-11-19

- republish: version-manifest bump only, no code changes since v1.7.20

## v1.7.20 — 2026-11-19

- python sdk: clear the socket timeout after the handshake - idle tunnels no longer drop every 15-45s (#241) (PR #244)
- rust sdk: arm the 200ms read tick after the welcome frame, not before it - slow networks no longer fail the handshake spuriously (#242) (PR #245)

## v1.7.19 — 2026-11-19

- stats loopback-only without admin token (#234); mesh peer DNS off gossip lock + gossip port fail-fast (#238); ACME renewal-window startup falls back to cached cert (#239)

## v1.7.18 — 2026-11-19

- payments: NOWPayments IPN signature now verified over json.Number raw literals - every real crypto-payment IPN no longer 400s (#216); underpayment with mismatched types fails closed instead of open (#222) (PR #226)
- payments: paid-activation persistence failures return an error and the webhook answers 5xx so the provider retries (#218); redundant double-write removed (PR #227)
- sdk: ConnectReconnecting accepts multi-target routes - -route mode starts by default (#229) (PR #233)

## v1.7.17 — 2026-11-19

- deps upgrade: go-webauthn/x 0.4.1, tungstenite 0.30, gh-actions-lua v13 (#211)

## v1.7.16 — 2026-11-18

- audit batch: 19 review fixes (mesh gossip auth #186 - BREAKING: mesh deployments must set ETUNNEL_MESH_SECRET; webhook header stripping #187; HEAD stream leak #188; payment webhook 5xx #198; PG upsert logging #199; short subdomain claim #197; strict tiers parsing #196; return_to validation #189; bounded register body #193)
- build: make verify now runs go test -race (#202)
- docs: protocol/README sync with the above (mesh secret noted as breaking)

## Unreleased

- **security**: mesh gossip is now authenticated and encrypted via a required
  `ETUNNEL_MESH_SECRET`; the router additionally verifies that ownership claims
  come from known members and bounds the owner map (#186, #191). **Breaking for
  existing mesh deployments**: set the variable on every node before upgrading.
- **security**: the edge strips inbound `X-Etunnel-*` headers, so the
  `verified:` webhook marker can no longer be spoofed by a visitor (#187)
- **fix**: HEAD responses no longer park a reader in `bodyPr.Read` (goroutine +
  connection leak) and now flush their headers (#188)
- **fix**: tunnel-client supplied `res.status` is clamped to 502 instead of
  panicking `net/http` (#200)
- **fix**: generated 404/502 pages HTML-escape the visitor-controlled Host
  header (#194)
- **fix**: graceful shutdown drains sessions, waits for `bye` to flush, and logs
  listener errors instead of discarding them (#192)
- **docs**: subdomain length rules are consistent end to end (1-63), so paid
  1-2 character subdomains are usable (#197)
- **fix**: CSPRNG failure while generating a subdomain now fails loudly instead
  of silently degrading entropy (#201)
- **fix**: `return_to` rejects backslash and absolute-URL bypasses (#189)
- **fix**: `/me` emits the email as a JSON string literal, not `%q` inside
  `template.JS` (#195)

## v1.7.15 — 2026-11-18

- swift: supervised reconnect wrapper + observable tunnel state (Reconnect.swift, #183)

## v1.7.14 — 2026-11-18

- patch maintenance release (no content deltas since v1.7.13)

## v1.7.13 — 2026-11-18

- deps upgrade: x/net 0.63 + zeroize 1.9.2 (#184)

## v1.7.12 — 2026-11-17

- ci: vet + gofmt mechanical gates wired into make verify / tag build (#181)
- swift: scope xdg-open Process.run to os(Linux) for iOS portability (#182)

## v1.7.11 — 2026-11-17

- gap docs narrowing (#178); deps upgrade puddle patch + rust lock sync (#179)

## v1.7.10 — 2026-11-16

- cmd: drop the blind ready log line; CLI version now injected via ldflags at build (#175)

## v1.7.9 — 2026-11-16

- rubygems CI oracle fix (#172); systemd template unit parameterization + hardening (#174)

## v1.7.8 — 2026-11-16

- helm chart: pod securityContext hardening defaults (#171)
- release tooling: CHANGELOG gap-fill + bump auto-append (#169); bump wiring-order fix + burned v1.7.7 (broken staging, per double-skip protocol)

## v1.7.0 — 2026-11-15

Multi-target routing across all SDKs; hardening.

- **Multi-target local routing** (#148/#153): one public hostname, several
  local services (path-based routing) — Go SDK first, then all 20 SDKs,
  docs and a runnable demo.
- Demo-gate follow-ups #149-#152 (#154) and the 302 strip-mode pitfall
  note in every SDK README (#155/#156).
- **Release staging guard** (#157/#158): `make verify` refuses a dirty
  tree; release flow double-checks every manifest inside the tag before
  pushing.

## v1.7.2 — 2026-11-15

- **Error-code registry** (#159): the full 11-code table lands in
  PROTOCOL.md; the last bare errors get `et_` prefixes.

## v1.7.4 — 2026-11-15

Error handling and the Swift package restructure.

- **SDK-local error codes** (#161): `sdk_` namespace (sdk_connect /
  sdk_timeout / sdk_parse) — Go sentinel errors (`errors.Is`-able),
  `err.code` on node; PROTOCOL.md documents the local-code section.
- payments idempotency tests isolated per-process (#147).
- **Swift Package Index restructure** (#161): library product `etunnel`
  + executable `etunnel-cli` split — SPI compatibility matrix rebuilds
  from this tag.

## v1.7.5 — 2026-11-15

- deno/typescript `err.code` parity with node (#164); stale swift example
  CLI name fixed.
- **Connect refactor** (#162/#165): handshake carved out; complexity 24
  → 16, behavior byte-identical.
- PROTOCOL.md local-code wording: node, deno and ts attach `err.code` (#166).

## v1.7.6 — 2026-11-16

- **Dependency upgrade** (#167): actions/setup-python v5 → v7, rust
  `cc` 1.11.2, lockfile resync; go/npm/python/elixir already latest.

## v1.6.0 — 2026-11-11

Site i18n, OAuth reliability, premium short domains & hardening follow-up.

### Server (etunneld)

- **11-language user site** (#72/#73/#75): landing, pricing, billing, login,
  `/me` and the legal pages' chrome localized (ar renders RTL); language
  negotiated from the `et_lang` cookie, then `Accept-Language` with
  q-values, then English. Legal document bodies stay English (governing).
- **OAuth logins fixed end-to-end** (#83/#86): the state cookie is now
  SameSite per provider (Lax for redirect flows - None cookies were dropped
  by third-party-cookie blocking and every provider failed "bad oauth
  state"), and auth flows canonicalize onto the service host so a login
  started on the apex no longer loses its host-only cookie.
- **Premium 1-3 character subdomains** (#82): $3/$2/$1 (one-time per 30-day
  period, crypto floors at $2); Free accounts may buy; entitlement is
  exact-length and survives tier lapse.
- **Subdomain UX** (#79/#80/#81): 1-63 chars, pasted hostnames normalized,
  debounced availability probe with inline status, taken names block the
  Add button, cross-account duplicates rejected (409), and a toast surfaces
  every action error that used to be silent (#87).
- **Anonymous tunnel names carry 2^40.6 of entropy** (#84): the old
  word-word-NN format was exhaustively scannable in under an hour; the
  new 6-char no-lookalike suffix needs millennia.
- **Crypto pricing above the processor floor** (#78): NOWPayments minimums
  (~$2-5 per coin pair) made the $3 Starter unpayable on crypto; the crypto
  path now carries its own price (Starter $5) shown on the button.
- **Rendering** (#76): i18n inline HTML renders verbatim (identity lines,
  pricing hint) while user-controlled values stay escaped.
- **Terms** (#77): explicit unlawful-use ban (fraud, trafficking, sanctions
  evasion, unlicensed regulated trade) with termination and cooperation
  clauses; legal-page chrome localized.
- **Session ledger & observability** (#70): session open/close with source
  IP and disconnect reason in journal + Postgres; proxy upstream-failure
  logging and a first-requests regression test (#68).
- **Security hygiene**: pentest report redacted and ignored; short-domain
  gating exact-length; release script refuses non-semantic versions.

### Docs & SDKs

- README: localized-site section, `/setlang` and `/me/domain-check`
  endpoints, premium short-domain checkout products, high-entropy example
  names.
- All 19 SDK READMEs link the service terms and privacy policy (#commit).

## v1.5.0 — 2026-11-11

Pentest-hardening & SDK quality release. Full external penetration test
findings fixed; all 20 SDKs reviewed line-by-line, 11 HIGH issues closed.

### Server (etunneld)

- **Reserved paths no longer shadow tunnel subdomains** (#44/#35): `/login`,
  `/healthz`, `/api/*`, `/pricing`, `/me`, `/tunnel`, `/webhooks/*` now serve
  the tunnelled service on tunnel subdomains; etunneld's own endpoints live
  on the base domain only.
- **Non-reflecting 80→443 redirect** (#44/#37): unknown Host headers get 421,
  base hosts redirect to the canonical domain.
- **Security headers** on etunneld's own pages (#44/#41): HSTS, XFO DENY,
  nosniff, CSP frame-ancestors.
- **Per-IP quota keys expire** (#43/#36): rolling 24h TTL; all six
  handshake-rejection paths release their slot; counters DEL at zero
  (Redis via Lua, atomically).
- **Webhook idempotency + buyer-owned activation** (#45/#39/#40).
- **No /assets/ directory listing; base-domain access log; admin auth
  logging** (#46/#38/#42).

### SDKs (all 20 reviewed)

- **C++/Elixir now verify TLS by default** (#57/#49, #58/#50) — previously
  effectively `verify_none` (MITM exposure).
- C: sends queued through the service loop (UAF fix), escaped hello,
  owned stream slots, bounded writes (#60/#48).
- C#: serialized WS sends, no HttpClient leaks, working one-shot signals
  (#66/#54).
- Rust/Swift/Dart: unblocked sends, no force-unwrap crashes, clamped
  frames (#67/#53).
- Elixir: working resume chain + structured errors + real close (#58/#50).
- JVM: Kotlin close, welcome timeouts, Java escape order, NO_PROXY,
  shared clients, 1.4.0→1.5.0 alignment (#59/#51).
- Script pack: node self-dep, lua rockspec/unescape, ruby library-safety,
  php metadata, perl regex, ts/deno types (#61/#52).
- Go SDK: context cancelled on every error path, protocol constants for
  permanent errors (`subdomain_invalid`), reconnect docs (#56/#55).

## v1.4.0 — 2026-11-10

Multi-instance cluster layer. One binary, three modes; **single-node
deployments keep byte-identical behavior with zero config changes**.

### Server (etunneld)

- **Cluster modes** — `ETUNNEL_CLUSTER=single` (default) | `mesh` | `sticky`.
  - `mesh`: memberlist gossip ownership + cross-peer forwarding; works
    behind any L4 load balancer (round-robin fine).
  - `sticky`: Redis routing table (`et:route:<sub>`, 15s TTL lease, 5s
    heartbeat) for L7 load balancers.
- **Postgres account store** — `ETUNNEL_PG_DSN`; in-memory hot path with
  write-through + 5s poll; auto-migrated schema. Without it: JSON store
  as before.
- **Redis global counters** — `ETUNNEL_REDIS_URL`; exact global per-IP /
  per-account limits. Without it: per-instance limits as before.
- **Graceful drain** — `/healthz` flips 503 on SIGTERM, sessions drain,
  SDKs re-land on any peer with the same URL (resume tokens verify via
  shared `SESSION_SECRET`).
- **Fix**: brand-asset file server was claiming the global `/assets/`
  subtree — a tunneled WebUI's own `/assets/*.js|css` returned Go's
  default 404 while the homepage proxied fine. Now host-scoped to the
  base domain only.

### Deployment artifacts

- Docker image (distroless, ~17MB) + `deploy/docker-compose.yml` (3-node
  mesh + PG + Redis, verified end-to-end).
- Helm chart `deploy/helm/etunnel` (StatefulSet + headless DNS, verified
  on k3s: 3-pod mesh, `K3S-CLUSTER-OK` x3).
- Bare-metal systemd template `deploy/systemd/etunneld-cluster@.service`.
- Full guide with mermaid diagrams: `deploy/CLUSTER.md`.

### CI

- Every build runs the live PG/Redis cluster-store tests against service
  containers (postgres 16 + redis 7) and **fails on SKIP**, so store
  coverage can never silently regress.

### SDKs

- Go: reconnect-policy unit tests (grow/jitter/isPermanent).
- No API changes — all 20 SDKs at 1.4.0.

### Upgrade notes

- Single-instance users: drop-in binary replacement; no new env vars
  required.
- To enable the cluster layer see `deploy/CLUSTER.md` — the only required
  secret in any multi-instance mode is `SESSION_SECRET`.

## v1.3.1

- Hero-video landing page, billing/limits hardening (see git history).

## v1.3.0

- Resume tokens, reconnect-with-same-subdomain, OAuth account tiers
  (see git history).
