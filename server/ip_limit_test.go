package server

// Regression tests for #232: the anonymous per-IP tunnel limit used to
// fail open while the shared counter was down, and a phantom INCR left a
// no-TTL Redis key that permanently locked a single-tunnel IP out.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// flakyCounter mimics a broken Redis: every Incr fails (the INCR may still
// have landed server-side), Decr succeeds and is recorded.
type flakyCounter struct{ decrs chan string }

func newFlakyCounter() *flakyCounter { return &flakyCounter{decrs: make(chan string, 8)} }

func (f *flakyCounter) Incr(ctx context.Context, key string, delta int64) (int64, error) {
	return 0, errors.New("backend down")
}
func (f *flakyCounter) Decr(ctx context.Context, key string) error {
	select {
	case f.decrs <- key:
	default:
	}
	return nil
}
func (f *flakyCounter) Get(ctx context.Context, key string) (int64, error) {
	return 0, nil
}
func (f *flakyCounter) SetTTL(ctx context.Context, key string, d time.Duration) error {
	return nil
}

func TestIPLimitDegradesInsteadOfFailingOpen(t *testing.T) {
	s := &Server{cfg: Config{MaxTunnelsPerIP: 1}, counter: newFlakyCounter()}
	ctx := context.Background()

	c1 := s.takeIPSlot(ctx, "198.51.100.7")
	if c1 == nil {
		t.Fatal("first handshake rejected while degraded (limit vanished = #232 regression)")
	}
	if c2 := s.takeIPSlot(ctx, "198.51.100.7"); c2 != nil {
		t.Fatal("second handshake from the same IP granted: degraded limit not enforced")
	}
	// releasing the slot frees the next one
	_ = c1.Decr(ctx, "ip:198.51.100.7")
	if c3 := s.takeIPSlot(ctx, "198.51.100.7"); c3 == nil {
		t.Fatal("slot not reusable after release")
	}
}

func TestIPLimitCompensatesPhantomIncr(t *testing.T) {
	f := newFlakyCounter()
	s := &Server{cfg: Config{MaxTunnelsPerIP: 1}, counter: f}
	if c := s.takeIPSlot(context.Background(), "198.51.100.8"); c == nil {
		t.Fatal("degraded grant failed")
	}
	select {
	case key := <-f.decrs:
		if key != "ip:198.51.100.8" {
			t.Fatalf("compensated wrong key %q", key)
		}
	case <-time.After(time.Second):
		t.Fatal("phantom INCR never compensated: a no-TTL slot can lock the IP out forever (#232)")
	}
}

func TestIPLimitSharedHappyPath(t *testing.T) {
	mem := newMemCounter()
	s := &Server{cfg: Config{MaxTunnelsPerIP: 1}, counter: mem}
	ctx := context.Background()
	if c := s.takeIPSlot(ctx, "198.51.100.9"); c == nil {
		t.Fatal("first grant failed")
	}
	if c := s.takeIPSlot(ctx, "198.51.100.9"); c != nil {
		t.Fatal("over-limit grant")
	}
	if v, _ := mem.Get(ctx, "ip:198.51.100.9"); v != 1 {
		t.Fatalf("rejected handshake changed the count: %d", v)
	}
}

// The session must return its slot to the ledger that granted it, so a
// slot granted from the local fallback does not decrement the shared one.
func TestSessionCloseReturnsSlotToGrantingCounter(t *testing.T) {
	mem := newMemCounter()
	s := newHOLSession(t)
	s.id = "slot-test"
	s.ip = "198.51.100.10"
	fallback := newMemCounter()
	if _, err := fallback.Incr(context.Background(), "ip:198.51.100.10", 1); err != nil {
		t.Fatal(err)
	}
	s.srv = &Server{counter: mem, reg: &Registry{}}
	s.ipCounter = fallback
	s.Close("test")
	if v, _ := mem.Get(context.Background(), "ip:198.51.100.10"); v != 0 {
		t.Fatalf("shared counter decremented for a fallback-granted slot: %d", v)
	}
	if v, _ := fallback.Get(context.Background(), "ip:198.51.100.10"); v != 0 {
		t.Fatalf("fallback slot not released: %d", v)
	}
}
