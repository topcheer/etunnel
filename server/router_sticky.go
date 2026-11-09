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
	"net/http"
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
			r.mu.RLock()
			subs := make([]string, 0, len(r.ours))
			for s := range r.ours {
				subs = append(subs, s)
			}
			r.mu.RUnlock()
			for _, s := range subs {
				r.rdb.Set(ctx, "et:route:"+s, r.self, routeLeaseTTL)
			}
		}
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
	v, err := r.rdb.Get(r.ctx0, "et:route:"+sub).Result()
	if err != nil || v == r.self {
		return ""
	}
	return v
}

// Claim writes the route with a lease.
func (r *stickyRouter) Claim(sub string) {
	r.mu.Lock()
	r.ours[sub] = true
	r.mu.Unlock()
	r.rdb.Set(r.ctx0, "et:route:"+sub, r.self, routeLeaseTTL)
}

// Release deletes the route.
func (r *stickyRouter) Release(sub string) {
	r.mu.Lock()
	delete(r.ours, sub)
	r.mu.Unlock()
	r.rdb.Del(r.ctx0, "et:route:"+sub)
}

// Forward proxies to the owner (mixed-mode deployments; normally the LB does it).
func (r *stickyRouter) Forward(sub string, w http.ResponseWriter, req *http.Request) bool {
	if req.Header.Get("X-Etunnel-Forward") != "" {
		return false // loop guard
	}
	target := r.Lookup(sub)
	if target == "" {
		return false
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
	for k, vs := range resp.Header {
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
		r.rdb.Del(context.Background(), keys...)
	}
	return r.rdb.Close()
}
