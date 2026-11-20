# Changelog

## v1.7.22 — 2026-11-20

- (fill in)

## v1.7.21 — 2026-11-19

- (fill in)

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
