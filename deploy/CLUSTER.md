# etunneld multi-instance deployment

One binary, three cluster modes - pick per environment. The resume-token
design makes client failover free: any instance can validate a reconnecting
SDK (shared `SESSION_SECRET`) and hand back the same subdomain, so visitors
never see the switch.

## Modes

| Mode | env | Routing | Best for |
|---|---|---|---|
| single | `ETUNNEL_CLUSTER=single` (default) | local registry | today's single node; zero behavior change |
| mesh | `ETUNNEL_CLUSTER=mesh` | gossip + cross-peer forward | Docker Compose, k3s/k8s StatefulSet, bare metal behind any L4 LB |
| sticky | `ETUNNEL_CLUSTER=sticky` | Redis routing table (`et:route:<sub>`, TTL lease) | L7 LB that can read Redis (nginx lua / traefik plugin) |

## Shared services

- `ETUNNEL_PG_DSN` - accounts in Postgres (auto-migrated schema, 5s poll). Without it: local JSON store (single writer only).
- `ETUNNEL_REDIS_URL` - exact global per-IP / per-account limits. Without it: per-instance limits.
- `SESSION_SECRET` - required in any multi-instance mode: signs session cookies AND resume tokens.
- `ETUNNEL_ADVERTISE` / `ETUNNEL_JOIN` - mesh only; gossip port is advertise+100 (or `ETUNNEL_GOSSIP_PORT`).

## Verified deployments

| Environment | Artifact | Evidence |
|---|---|---|
| Docker Compose | `deploy/docker-compose.yml` (3 nodes + PG + Redis) | SDK->node1, visitors on ANY node get `CLUSTER-FINAL` |
| k3s | `deploy/helm/etunnel` (StatefulSet, 3 pods) | same via ClusterIP, `K3S-CLUSTER-OK` x3 |
| Bare metal | `deploy/systemd/etunneld-cluster@.service` x3 on one host | same over loopback, `200` x3 |

## Scaling notes

- Horizontal: `kubectl scale sts` / add `systemctl enable etunneld-cluster@N` / compose `--scale`; mesh converges in seconds.
- Rolling updates: `/healthz` flips to 503 on SIGTERM (Drain) before sessions close; SDKs reconnect to a surviving peer with the same URL.
- Limits: with Redis the per-IP cap is global (a shared IP cannot open N x replicas tunnels).
- A dead instance's subdomains: mesh re-broadcasts ownership and leases (sticky) expire; clients re-land on any peer.

## Minimum infrastructure per mode

### single

| Requirement | Why |
|---|---|
| 1 host, 1 port (443) | the whole server |
| writable disk for JSON + ACME | accounts + certs |
| _nothing else_ | zero external dependencies |

### mesh

| Requirement | Why |
|---|---|
| each instance reachable from peers on `advertise` port (HTTP) + `advertise+100` (gossip, TCP+UDP) | forwarding + membership |
| a seed address (`ETUNNEL_JOIN`) for instances 2..N | join the gossip cluster |
| **Postgres** (recommended) | accounts across instances; without it each instance keeps its own JSON file = accounts only exist on the node that created them |
| **Redis** (recommended) | exact global limits; without it limits are per-instance (N replicas = N x the cap) |
| `SESSION_SECRET` (required) | resume tokens must verify on any instance |
| any L4 load balancer (round-robin is fine) | wrong landing is corrected by internal forwarding |

### sticky

| Requirement | Why |
|---|---|
| **Redis** (required) | the routing table lives there |
| **L7 load balancer that reads Redis** (nginx + lua, traefik plugin, or a custom sync loop) | routing decision is made by the LB, not the instances |
| **Postgres** (recommended) | same as mesh |
| `SESSION_SECRET` (required) | same as mesh |
| instances do NOT need to reach each other | the LB routes directly |

## Limitations and failure behavior

### Both modes

- **PG down**: instances keep serving from their in-memory cache (loaded at
  boot + 5s poll). New account creation fails until PG returns; existing
  tunnels and lookups keep working. Writes made while PG is down are lost
  (no write-behind queue).
- **SESSION_SECRET rotation**: all OAuth sessions and outstanding resume
  tokens invalidate. Users log in again; reconnecting SDKs get a new random
  subdomain (in-flight links break until re-shared). Plan rotations.
- **JSON store + replicas**: never combine. JSON is single-writer; with
  mesh + JSON each node has its own account universe.
- **Limits without Redis**: per-instance. `max-tunnels-per-ip 5` across
  3 instances allows up to 15 from one IP.
- **Anonymous per-IP counter leak**: an instance killed with SIGKILL (not
  SIGTERM) leaves its Redis counter entry behind; it expires only via the
  TTL (set by the harness) or manual cleanup. SIGTERM drains cleanly.

### mesh

- **Gossip convergence window**: a brand-new tunnel is invisible to peers
  for up to ~1s (direct claim message) or ~10s (rebroadcast) if the direct
  message is lost. A visitor hitting another node in that window gets a 404
  until convergence. Mitigated by the rebroadcast; not eliminated.
- **Network partition**: two partitions each hand out the same random
  subdomain with probability ~0 (adjective-noun-nn space). Custom
  subdomains are claimed per-host in Postgres... actually no: custom
  subdomain claims are still registry-local in this version. **A partition
  can give the same CUSTOM subdomain to two clients** - do not run mesh
  across a partition-prone WAN without sticky+PG.
- **Forwarding adds one hop**: a misrouted visitor request pays one
  extra internal hop (~1ms LAN). Steady state >99% of requests land
  directly on the owner.
- **Memberlist port**: gossip needs TCP **and** UDP on advertise+100. Docker
  `EXPOSE`/k8s Service must include the UDP port (the Helm chart does).

### sticky

- **LB is the single routing brain**: if the LB cannot read Redis, routing
  breaks entirely. This is the operational cost of the mode.
- **Lease granularity**: a route lives 15s without a heartbeat. A stopped
  (SIGKILL) instance's subdomains accept misrouted traffic for up to 15s
  before the lease expires; the SDK reconnect fixes it client-side.
- **No cross-instance forwarding loop protection needed** (the LB routes
  directly), but the shared Forward method is still available for mixed
  deployments - the `X-Etunnel-Forward` loop guard applies.

### Known non-goals (v1 of the cluster layer)

- No automatic leader election (none needed - every instance is equal).
- No multi-region replication of Postgres/Redis - single PG/Redis is the
  write ceiling. For geo-redundancy, run independent clusters per region.
- Custom subdomain uniqueness across instances is **not** enforced by a
  distributed lock in this version (see mesh partition note above).
- Session (tunnel) state is never migrated between instances; recovery is
  always client-side reconnect via resume token.
