# Changelog

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
