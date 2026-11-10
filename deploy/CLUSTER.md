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
