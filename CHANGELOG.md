# Changelog

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
