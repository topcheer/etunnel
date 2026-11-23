package server

// #395: DeleteWithError used to write its tombstone AFTER the PG round trip
// (post-unlock). A loadAll pass whose sweep ran inside the Exec window (a
// network IO stall) carried the doomed row into the swap and resurrected the
// deleted account - its revoked API key kept authenticating - until the next
// poll. These tests freeze the delete mid-flight through the deleteExec seam
// and race a real sweep+swap against it, asserting the tombstone precedes
// the Exec and that a failed delete retracts it.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPGDeleteTombstonesBeforeExec(t *testing.T) {
	k1 := &Account{APIKey: "k1", Email: "k1@x.com"}
	s := &pgAccountStore{
		byKey:  map[string]*Account{"k1": k1},
		byMail: map[string]*Account{"k1@x.com": k1},
	}
	execStarted := make(chan struct{})
	releaseExec := make(chan struct{})
	var execCalls atomic.Int32
	s.deleteExec = func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
		if execCalls.Add(1) == 1 {
			close(execStarted)
		}
		<-releaseExec // frozen mid-flight: DELETE not yet committed
		var tag pgconn.CommandTag
		return tag, nil
	}

	done := make(chan error, 1)
	go func() { done <- s.DeleteWithError("k1") }()

	select {
	case <-execStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("delete never reached Exec")
	}

	// A loadAll pass in flight around the same moment: its fresh PG snapshot
	// still contains the row (the DELETE has not committed yet). The sweep
	// must drop it because the tombstone was written BEFORE the Exec. (The
	// swap below happens after the delete returns, mirroring reality: the
	// swap needs s.mu, which DeleteWithError holds across the Exec.)
	fresh := map[string]*Account{"k1": k1}
	freshMail := map[string]*Account{"k1@x.com": k1}
	s.sweepTombstones(fresh, freshMail)
	if _, ok := fresh["k1"]; ok {
		t.Fatal("loadAll sweep inside the Exec window kept the doomed row (#395: tombstone must precede Exec)")
	}

	close(releaseExec)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("delete failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delete never finished")
	}
	// Now the racing loadAll performs its swap. Under the old order its
	// unfiltered snapshot resurrected the deleted account here; with the
	// tombstone-first order the swept maps already lack k1.
	s.mu.Lock()
	s.byKey, s.byMail = fresh, freshMail
	s.mu.Unlock()
	s.mu.RLock()
	_, live := s.byKey["k1"]
	_, liveMail := s.byMail["k1@x.com"]
	s.mu.RUnlock()
	if live || liveMail {
		t.Fatal("deleted account resurrected in the live maps (#395)")
	}
	if got := execCalls.Load(); got != 1 {
		t.Fatalf("execDelete called %d times, want 1", got)
	}
}

func TestPGDeleteExecFailureRetractsTombstone(t *testing.T) {
	k1 := &Account{APIKey: "k1", Email: "k1@x.com"}
	s := &pgAccountStore{
		byKey:  map[string]*Account{"k1": k1},
		byMail: map[string]*Account{"k1@x.com": k1},
	}
	s.deleteExec = func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
		return pgconn.CommandTag{}, context.DeadlineExceeded
	}
	if err := s.DeleteWithError("k1"); err == nil {
		t.Fatal("expected the Exec failure to surface (#366 contract)")
	}
	// The in-memory account must survive a failed delete (#366).
	s.mu.RLock()
	_, still := s.byKey["k1"]
	s.mu.RUnlock()
	if !still {
		t.Fatal("failed delete removed the live in-memory account (#366)")
	}
	// And the early tombstone must be retracted: a loadAll sweep must keep
	// the still-live row instead of hiding it for the TTL.
	fresh := map[string]*Account{"k1": k1}
	freshMail := map[string]*Account{"k1@x.com": k1}
	s.sweepTombstones(fresh, freshMail)
	if _, ok := fresh["k1"]; !ok {
		t.Fatal("failed delete left a tombstone hiding a live row from loadAll (#395)")
	}
}
