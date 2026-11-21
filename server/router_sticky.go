package server

// Sticky router (cluster mode A): instances publish subdomain ownership into a
// shared Redis routing table that an L7 load balancer reads to route by Host.
//
// Enable with ETUNNEL_CLUSTER=sticky + ETUNNEL_REDIS_URL. Keys:
//
//	et:route:<subdomain> = "<advertiseAddr>"  (TTL lease, renewed by heartbeat)
//
// A dead instance's routes expire with the lease: the SDK reconnects (the
// resume token is instance-agnostic), any instance accepts it, and the table
// repoints.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const routeLeaseTTL = 15 * time.Second // renewed every 5s by the heartbeat

type stickyRouter struct {
	rdb    *redis.Client
	self   string
	mu     sync.RWMutex
	ours   map[string]bool
	cancel context.CancelFunc
	hc     *http.Client
	ctx0   context.Context
	// Injectable Redis seams (#236): the concrete client cannot be faked
	// without a live server, so the routing ops go through these funcs.
	setRoute func(ctx context.Context, key string, val string, ttl time.Duration) error
	getRoute func(ctx context.Context, key string) (string, error)
	delRoute func(ctx context.Context, keys ...string) error
	// hbFails counts consecutive failed heartbeat ticks (guarded by mu).
	// Three in a row (~15s, one lease) relinquish local ownership (#236).
	hbFails int
}

func newStickyRouter(redisURL, advertise string) (*stickyRouter, error) {
	rc, err := newRedisCounter(redisURL) // reuse URL parsing + ping
	if err != nil {
		return nil, err
	}
	r := &stickyRouter{
		rdb:  rc.rdb,
		self: advertise,
		ours: map[string]bool{},
		hc:   &http.Client{Timeout: 30 * time.Second},
	}
	r.setRoute = func(ctx context.Context, key, val string, ttl time.Duration) error {
		return r.rdb.Set(ctx, key, val, ttl).Err()
	}
	r.getRoute = func(ctx context.Context, key string) (string, error) {
		return r.rdb.Get(ctx, key).Result()
	}
	r.delRoute = func(ctx context.Context, keys ...string) error {
		return r.rdb.Del(ctx, keys...).Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.ctx0 = ctx
	r.cancel = cancel
	go r.heartbeat(ctx)
	return r, nil
}

func (r *stickyRouter) heartbeat(ctx context.Context) {
	t := time.NewTicker(routeLeaseTTL / 3)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.heartbeatTick(ctx)
		}
	}
}

// heartbeatTick renews every owned lease once and tracks consecutive
// failures. Redis swallowing lease renewals is how a live node ends up
// "expiring" into split-brain (#236): after three failed ticks (~one lease
// lifetime) the node relinquishes its ownership so peers take over
// cleanly instead of racing a resurrected key.
func (r *stickyRouter) heartbeatTick(ctx context.Context) {
	r.mu.RLock()
	subs := make([]string, 0, len(r.ours))
	for s := range r.ours {
		subs = append(subs, s)
	}
	r.mu.RUnlock()
	if len(subs) == 0 {
		r.mu.Lock()
		r.hbFails = 0
		r.mu.Unlock()
		return
	}
	var firstErr error
	failed := 0
	for _, s := range subs {
		if err := r.setRoute(ctx, "et:route:"+s, r.self, routeLeaseTTL); err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	r.mu.Lock()
	if failed > 0 {
		r.hbFails++
	} else {
		r.hbFails = 0
	}
	fails := r.hbFails
	r.mu.Unlock()
	if failed > 0 {
		log.Printf("etunneld: sticky: heartbeat failed for %d/%d route(s): %v", failed, len(subs), firstErr)
	}
	if fails == 3 {
		log.Printf("etunneld: sticky: %d consecutive heartbeat ticks failed; relinquishing %d owned route(s) so peers take over cleanly", fails, len(subs))
		r.mu.Lock()
		r.ours = map[string]bool{}
		r.mu.Unlock()
	}
}

// Lookup returns the owner's advertise address, "" when we own it or the
// route is unknown (expired lease: any instance may accept).
func (r *stickyRouter) Lookup(sub string) string {
	r.mu.RLock()
	own := r.ours[sub]
	r.mu.RUnlock()
	if own {
		return ""
	}
	v, err := r.getRoute(r.ctx0, "et:route:"+sub)
	if err == nil && v != r.self {
		return v
	}
	// #236: a missing key genuinely means "nobody owns it"; any other
	// error means the routing table is UNREADABLE - surface it, because
	// returning "" here lets any instance accept the subdomain and is the
	// front door to a split-brain.
	if err != nil && !errors.Is(err, redis.Nil) {
		log.Printf("etunneld: sticky: route lookup for %s failed (treated as unowned): %v", sub, err)
	}
	return ""
}

// Claim writes the route with a lease. An error means the cluster cannot
// see this ownership: the caller must fail the handshake rather than run
// a tunnel only this node knows about (#236).
func (r *stickyRouter) Claim(sub string) error {
	if err := r.setRoute(r.ctx0, "et:route:"+sub, r.self, routeLeaseTTL); err != nil {
		log.Printf("etunneld: sticky: claim %s failed: tunnel would be invisible to the cluster: %v", sub, err)
		return fmt.Errorf("etunnel: claim route %s: %w", sub, err)
	}
	r.mu.Lock()
	r.ours[sub] = true
	r.mu.Unlock()
	return nil
}

// Release deletes the route. Best effort by design (the lease expires
// anyway), but failures are logged instead of silently swallowed (#236).
func (r *stickyRouter) Release(sub string) {
	r.mu.Lock()
	delete(r.ours, sub)
	r.mu.Unlock()
	if err := r.delRoute(r.ctx0, "et:route:"+sub); err != nil {
		log.Printf("etunneld: sticky: release %s failed (key expires with the lease): %v", sub, err)
	}
}

// Forward proxies to the owner (mixed-mode deployments; normally the LB does it).
// Upgrade requests take the raw hijacked path: http.Client cannot carry a
// 101 both ways (#235).
// isInternalPeer reports whether the TCP peer address is on an internal
// network (loopback or RFC1918) - cluster peers and the LB live there;
// internet visitors do not (#295).
func isInternalPeer(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate()
}

func (r *stickyRouter) Forward(sub string, w http.ResponseWriter, req *http.Request) bool {
	// #295: the loop-guard header is set by cluster peers (internal
	// network). A public-internet visitor forging it could force a
	// selective 404; strip untrusted values before the check. Internal
	// peers (loopback / RFC1918 behind the LB) are trusted to set it.
	if req.Header.Get("X-Etunnel-Forward") != "" && !isInternalPeer(req.RemoteAddr) {
		req.Header.Del("X-Etunnel-Forward")
	}
	if req.Header.Get("X-Etunnel-Forward") != "" {
		return false // loop guard
	}
	target := r.Lookup(sub)
	if target == "" {
		return false
	}
	if hasUpgrade(req.Header) {
		return forwardUpgraded(target, w, req, "X-Etunnel-Forward", r.self)
	}
	out := req.Clone(req.Context())
	out.URL.Scheme = "http"
	out.URL.Host = target
	out.RequestURI = ""
	out.Header.Set("X-Etunnel-Forward", r.self)
	resp, err := r.hc.Do(out)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	// #235: hop-by-hop headers (Connection, Transfer-Encoding, ...) are
	// the transport's business; re-sending them verbatim corrupts the
	// visitor response (double TE / wrong Content-Length).
	tokens := connectionTokensFromMap(resp.Header)
	for k, vs := range resp.Header {
		if isHopHeader(k) || tokens[strings.ToLower(k)] {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	buf := make([]byte, 32*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return true
			}
		}
		if rerr != nil {
			return true
		}
	}
}

// Close deletes all our routes and stops the heartbeat.
func (r *stickyRouter) Close() error {
	r.cancel()
	r.mu.RLock()
	keys := make([]string, 0, len(r.ours))
	for s := range r.ours {
		keys = append(keys, "et:route:"+s)
	}
	r.mu.RUnlock()
	if len(keys) > 0 {
		if err := r.delRoute(context.Background(), keys...); err != nil {
			log.Printf("etunneld: sticky: close failed to delete %d route key(s) (they expire with their leases): %v", len(keys), err)
		}
	}
	if r.rdb == nil {
		return nil // constructed without a client (tests inject the seams)
	}
	return r.rdb.Close()
}
