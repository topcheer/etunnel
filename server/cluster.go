package server

// Cluster interfaces. The single-node implementations (JSON file, in-memory
// counters) preserve today's exact behaviour; the cluster implementations
// (Postgres, Redis, memberlist) are opt-in via env so a default binary run
// stays a self-contained single node with zero external dependencies.

import (
	"context"
	"net/http"
	"time"
)

// The AccountStore JSON implementation keeps its concrete methods; nothing
// in this file is implemented by it. (already satisfied by the JSON store).
// The Postgres store implements the same surface so no call site changes.
// (Defined here for documentation; the concrete types live in account.go and
// account_pg.go.)

// Counter is a distributed counter used for the two global quotas:
// tunnels-per-IP (anonymous abuse) and tunnels-per-account (plan limits).
// The in-memory implementation is authoritative on a single node; the Redis
// implementation makes the limits global across a cluster.
type Counter interface {
	// Incr adds delta to the key and returns the new value. Keys are
	// namespaced by the caller ("ip:<addr>", "acct:<apikey>").
	Incr(ctx context.Context, key string, delta int64) (int64, error)
	// Decr is Incr with a negative delta, used on disconnect.
	Decr(ctx context.Context, key string) error
	// Get returns the current value (0 when unknown).
	Get(ctx context.Context, key string) (int64, error)
	// TTL applies an expiry to idle per-IP counters so a churning IP does not
	// accumulate forever; per-account counters must use TTLNever.
	SetTTL(ctx context.Context, key string, d time.Duration) error
}

const (
	// TTLNever keeps the key until it is explicitly decremented to zero.
	TTLNever = time.Duration(0)
)

// PeerMeta is what one instance advertises to the cluster.
type PeerMeta struct {
	ID   string   `json:"id"`   // stable instance id (hostname or env)
	Addr string   `json:"addr"` // internal advertise address (host:port)
	Subs []string `json:"subs"` // subdomains this peer currently owns
}

// Router resolves which peer owns a subdomain and forwards when it is not us.
// meshRouter implements it via memberlist gossip; stickyRouter via a shared
// routing table (Redis) for L7 load balancers.
type Router interface {
	// Lookup returns the advertise address of the owner, or "" when we own it
	// or nobody does (fresh subdomain: we take it and broadcast).
	Lookup(sub string) string
	// Claim broadcasts that we now own sub.
	Claim(sub string)
	// Release broadcasts that we no longer own sub (going away or tunnel died).
	Release(sub string)
	// Forward proxies an HTTP exchange to the peer that owns sub.
	Forward(sub string, w http.ResponseWriter, r *http.Request) bool
	// Close leaves the cluster and broadcasts the release of everything.
	Close() error
}
