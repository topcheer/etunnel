package server

// #396 (LOW, two independent items from the 4h review):
//  1. Close must release a subdomain cluster-wide only for CLAIMED sessions:
//     a kick landing between registry acquire and the router claim must not
//     broadcast a mesh release that a peer would misapply to its own owner
//     mapping (#389 fixed the same class at the recheck; the kick path was
//     missed).
//  2. kickSessionsByAccount's async finisher is now tracked on Server.kickWG
//     so Run's shutdown can wait for it (bounded) instead of leaving a
//     goroutine writing recordClose into a closed pool.

import (
	"context"
	"testing"
	"time"
)

func TestCloseReleasesOnlyClaimedSessions(t *testing.T) {
	// Unclaimed: a session that entered the registry but was closed before
	// its handshake claim must NOT broadcast a release.
	spy := newSpyRouter()
	sess := newCloseTestSession(t, nil, spy)
	sess.sub = "preclaim396"
	sess.Close("test")
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		spy.mu.Lock()
		n := len(spy.ops["preclaim396"])
		spy.mu.Unlock()
		if n > 0 {
			t.Fatalf("unclaimed session broadcast a mesh release (#396): %v", spy.ops)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Claimed: a normal (post-welcome) close must release as before.
	spy2 := newSpyRouter()
	sess2 := newCloseTestSession(t, nil, spy2)
	sess2.sub = "claimed396"
	sess2.claimed.Store(true)
	sess2.Close("test")
	deadline = time.Now().Add(2 * time.Second)
	for {
		spy2.mu.Lock()
		ops := append([]string(nil), spy2.ops["claimed396"]...)
		spy2.mu.Unlock()
		if len(ops) > 0 && ops[len(ops)-1] == "release" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("claimed session never released its subdomain (#396 regression): %v", ops)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestKickFinisherTrackedByWaitGroup(t *testing.T) {
	s, _, _ := quotaTestServer(t, nil)
	// Each Close stalls ~2s in the per-IP Decr (bounded by Close's own
	// deadline, #220), so the 4th session overruns the 5s caller budget and
	// lands in the async finisher.
	slow := slowDecrCounter{delay: 2 * time.Second}
	rest := make([]*Session, 0, 4)
	for i := 0; i < 4; i++ {
		sess := &Session{
			srv:       s,
			id:        "kick396-" + string(rune('a'+i)),
			sub:       "kick396-" + string(rune('a'+i)),
			account:   &Account{APIKey: "kick396key"},
			closed:    make(chan struct{}),
			ipCounter: slow,
		}
		if err := s.reg.Acquire(sess.sub, sess); err != nil {
			t.Fatalf("acquire %s: %v", sess.sub, err)
		}
		rest = append(rest, sess)
	}

	n := s.kickSessionsByAccount("kick396key", "test kick")
	if n == 0 || n == len(rest) {
		t.Fatalf("kick closed %d sessions; want a sync majority and an async remainder", n)
	}
	// Run's shutdown waits on kickWG with a 2s grace; the finisher must land
	// the remainder well inside that. Assert via the WG itself (the exact
	// mechanism Run uses), bounded by the finisher's own 30s budget.
	done := make(chan struct{})
	go func() {
		s.kickWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("kick finisher never finished its remainder; kickWG tracking broken (#396)")
	}
	for _, sess := range rest {
		select {
		case <-sess.closed:
		default:
			t.Fatalf("session %s never closed by sync loop or finisher", sess.id)
		}
	}
}

// slowDecrCounter delays every Decr by a fixed duration, modelling a slow
// shared-counter backend (#232 counter contract).
type slowDecrCounter struct{ delay time.Duration }

func (c slowDecrCounter) Incr(ctx context.Context, key string, delta int64) (int64, error) {
	return 1, nil
}

func (c slowDecrCounter) Decr(ctx context.Context, key string) error {
	time.Sleep(c.delay)
	return nil
}

func (c slowDecrCounter) Get(ctx context.Context, key string) (int64, error) { return 0, nil }

func (c slowDecrCounter) SetTTL(ctx context.Context, key string, d time.Duration) error {
	return nil
}
