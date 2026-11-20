package server

// Regression tests for #236: stickyRouter swallowed every Redis routing
// error. A routing table is a correctness dependency: its silent failure
// meant split-brain tunnels, invisible ownership, and no signal at all.
// The injectable seams let these failure modes run without a live Redis.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func newTestSticky(set func(ctx context.Context, key, val string, ttl time.Duration) error,
	get func(ctx context.Context, key string) (string, error),
	del func(ctx context.Context, keys ...string) error) *stickyRouter {
	ctx, cancel := context.WithCancel(context.Background())
	t := &stickyRouter{
		self:   "n1:8080",
		ours:   map[string]bool{},
		ctx0:   ctx,
		cancel: cancel,
	}
	t.setRoute = set
	t.getRoute = get
	t.delRoute = del
	return t
}

func okSet(context.Context, string, string, time.Duration) error { return nil }
func okGet(context.Context, string) (string, error)              { return "", redis.Nil }
func okDel(context.Context, ...string) error                     { return nil }
func failSet(context.Context, string, string, time.Duration) error {
	return errors.New("redis down")
}

// A failed claim must be reported AND must not mark the route ours - the
// tunnel has to fail its handshake instead of running invisible to peers.
func TestStickyClaimFailureIsReportedAndNotOwned(t *testing.T) {
	r := newTestSticky(failSet, okGet, okDel)
	if err := r.Claim("quick-frog-42"); err == nil {
		t.Fatal("claim error swallowed (#236)")
	}
	r.mu.RLock()
	owned := r.ours["quick-frog-42"]
	r.mu.RUnlock()
	if owned {
		t.Fatal("failed claim still marked the route ours")
	}
}

// Three consecutive failed heartbeat ticks relinquish local ownership so
// peers take over cleanly instead of the lease expiring into split-brain.
func TestStickyHeartbeatRelinquishesAfterThreeFailedTicks(t *testing.T) {
	r := newTestSticky(failSet, okGet, okDel)
	r.ours["a-1"], r.ours["b-2"] = true, true
	for i := 0; i < 3; i++ {
		r.heartbeatTick(context.Background())
	}
	r.mu.RLock()
	n := len(r.ours)
	r.mu.RUnlock()
	if n != 0 {
		t.Fatalf("ownership not relinquished after 3 failed ticks: %d left", n)
	}
	// recovery resets the counter (and an empty ours no longer counts failures)
	r.setRoute = okSet
	r.heartbeatTick(context.Background())
	r.mu.RLock()
	fails := r.hbFails
	r.mu.RUnlock()
	if fails != 0 {
		t.Fatalf("failure streak not reset on recovery: %d", fails)
	}
}

// Lookup must treat redis.Nil as "nobody owns it" but surface real errors:
// both return "" (accept-and-claim semantics) but only the latter is a
// split-brain hazard, so it must not crash or be conflated with ownership.
func TestStickyLookupHandlesMissingAndBackendError(t *testing.T) {
	r := newTestSticky(okSet, func(context.Context, string) (string, error) {
		return "", redis.Nil
	}, okDel)
	if got := r.Lookup("x-1"); got != "" {
		t.Fatalf("missing key returned owner %q", got)
	}
	r.getRoute = func(context.Context, string) (string, error) {
		return "", errors.New("connection reset")
	}
	if got := r.Lookup("x-1"); got != "" {
		t.Fatalf("backend error returned owner %q", got)
	}
	r.getRoute = func(context.Context, string) (string, error) {
		return "n2:8080", nil
	}
	if got := r.Lookup("x-1"); got != "n2:8080" {
		t.Fatalf("healthy lookup returned %q", got)
	}
}

// Release/Close stay best effort but must survive backend failures.
func TestStickyReleaseAndCloseSurviveBackendErrors(t *testing.T) {
	r := newTestSticky(okSet, okGet, func(context.Context, ...string) error {
		return errors.New("del failed")
	})
	r.ours["c-3"] = true
	r.Release("c-3")
	r.mu.RLock()
	owned := r.ours["c-3"]
	r.mu.RUnlock()
	if owned {
		t.Fatal("release did not drop local ownership")
	}
	// Close with a broken del must still return without panic.
	if err := r.Close(); err == nil {
		t.Log("Close returned nil despite broken del (rdb.Close path)")
	}
}
