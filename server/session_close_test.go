package server

// Regression tests for #220: Session.Close ran blocking external I/O
// (mesh Release per-peer sends, Redis Decr with no deadline) inside the
// close Once, so every goroutine parked on that Once - and the shutdown
// path, serially - waited out backend flaps with the socket still open.

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// blockingCounter parks every Decr until its context is canceled.
type blockingCounter struct{}

func (blockingCounter) Incr(ctx context.Context, key string, delta int64) (int64, error) {
	return 1, nil
}

func (blockingCounter) Decr(ctx context.Context, key string) error {
	<-ctx.Done()
	return ctx.Err()
}

func (blockingCounter) Get(ctx context.Context, key string) (int64, error) { return 0, nil }

func (blockingCounter) SetTTL(ctx context.Context, key string, d time.Duration) error { return nil }

// blockingRouter parks every Release forever (worst-case mesh flap).
type blockingRouter struct{ released chan string }

func (b *blockingRouter) Lookup(sub string) string { return "" }
func (b *blockingRouter) Claim(sub string) error   { return nil }
func (b *blockingRouter) Release(sub string)       { <-make(chan struct{}) }
func (b *blockingRouter) Forward(sub string, w http.ResponseWriter, req *http.Request) bool {
	return false
}

func (b *blockingRouter) Close() error { return nil }

func newCloseTestSession(t *testing.T, counter Counter, router Router) *Session {
	t.Helper()
	s := newHOLSession(t)
	s.id = "close-test"
	s.ip = "203.0.113.9"
	s.srv = &Server{counter: counter, router: router, reg: &Registry{}}
	return s
}

func TestSessionCloseBoundedWhenCounterStalled(t *testing.T) {
	s := newCloseTestSession(t, blockingCounter{}, nil)
	done := make(chan struct{})
	go func() { s.Close("test"); close(done) }()
	select {
	case <-done:
		// bounded by the 2s deadline - well under the test's 3s bar
	case <-time.After(3 * time.Second):
		t.Fatal("Close blocked >3s on a stalled Redis counter (#220 regression)")
	}
}

func TestSessionCloseReturnsDespiteBlockedRelease(t *testing.T) {
	s := newCloseTestSession(t, nil, &blockingRouter{})
	s.sub = "stuck-frog-42" // force the Release branch
	done := make(chan struct{})
	go func() { s.Close("test"); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked on a stalled mesh Release (#220 regression)")
	}
}
