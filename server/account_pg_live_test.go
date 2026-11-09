package server

// Live test against the Postgres on host 13 (audiobook-postgres, database
// "etunnel" reserved for this project). Skipped when unreachable.

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func pgReachable(t *testing.T) bool {
	t.Helper()
	if err := exec.Command("nc", "-z", "-w", "2", "192.168.31.13", "5432").Run(); err != nil {
		t.Skip("pg at 13 not reachable")
		return false
	}
	return true
}

func TestPgAccountStoreLive(t *testing.T) {
	if !pgReachable(t) {
		return
	}
	dsn := os.Getenv("ET_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("ET_PG_TEST_DSN not set")
	}
	s, err := newPgAccountStore(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		s.pool.Exec(context.Background(), `DELETE FROM accounts WHERE email LIKE 'pgtest-%'`)
		s.Close()
	})
	ctx := context.Background()
	s.pool.Exec(ctx, `DELETE FROM accounts WHERE email LIKE 'pgtest-%'`)

	// create
	a, err := s.Create("pgtest-a@x.com", "pro")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	origKey := a.APIKey // copy: a is a shared pointer, ResetKey mutates it
	if a.APIKey == "" || a.Tier != "pro" {
		t.Fatalf("bad account %+v", a)
	}
	// duplicate email
	if _, err := s.Create("pgtest-a@x.com", "free"); err != ErrEmailTaken {
		t.Fatalf("dup email err = %v", err)
	}
	// lookup both ways
	if got, ok := s.Lookup(a.APIKey); !ok || got.Email != "pgtest-a@x.com" {
		t.Fatal("Lookup failed")
	}
	if got, ok := s.LookupEmail("pgtest-a@x.com"); !ok || got.APIKey != a.APIKey {
		t.Fatal("LookupEmail failed")
	}
	// mutate persists domains
	if !s.Mutate("pgtest-a@x.com", func(x *Account) { x.Domains = []string{"dev"} }) {
		t.Fatal("mutate")
	}
	// snapshot is a copy (key changed above; re-resolve)
	cur, _ := s.LookupEmail("pgtest-a@x.com")
	snap, _ := s.Snapshot(cur.APIKey)
	snap.Domains[0] = "tamper"
	if got, _ := s.Lookup(cur.APIKey); got.Domains[0] != "dev" {
		t.Fatal("Snapshot aliased Domains")
	}
	// reset key
	k2, ok := s.ResetKey("pgtest-a@x.com")
	t.Logf("orig=%q new=%q ok=%v", origKey, k2, ok)
	if !ok {
		t.Fatal("ResetKey returned !ok")
	}
	if k2 == origKey {
		t.Fatal("ResetKey returned the same key")
	}
	if _, ok := s.Lookup(origKey); ok {
		t.Fatal("old key still valid")
	}
	// expiry set + ExpiredPaid + DowngradeToFree keeps marker
	s.Mutate("pgtest-a@x.com", func(x *Account) { x.ExpiresAt = time.Now().Add(-time.Hour) })
	exp := s.ExpiredPaid(time.Now())
	if len(exp) != 1 || exp[0] != "pgtest-a@x.com" {
		t.Fatalf("ExpiredPaid -> %v", exp)
	}
	if !s.DowngradeToFree("pgtest-a@x.com") {
		t.Fatal("DowngradeToFree")
	}
	downgraded, _ := s.LookupEmail("pgtest-a@x.com")
	if downgraded.Tier != "free" || downgraded.LapsedAt.IsZero() {
		t.Fatal("lapse marker missing")
	}
	if !downgraded.EverPaid() {
		t.Fatal("EverPaid after lapse")
	}
	// reload from disk (fresh store) sees the same state
	s2, err := newPgAccountStore(dsn)
	if err != nil {
		t.Fatalf("second store: %v", err)
	}
	defer s2.Close()
	if got, ok := s2.Lookup(k2); !ok || got.Email != "pgtest-a@x.com" || !got.EverPaid() {
		t.Fatalf("reload lost state: %+v ok=%v", got, ok)
	}
	// prune never touches ever-paid
	if n := s.PruneInactive(time.Millisecond, time.Now()); n != 0 {
		t.Fatalf("prune deleted %d ever-paid accounts", n)
	}
	// free-only stale account IS prunable
	s.Create("pgtest-stale@x.com", "free")
	s.Mutate("pgtest-stale@x.com", func(x *Account) { x.Created = time.Now().Add(-time.Hour) })
	if n := s.PruneInactive(time.Millisecond, time.Now()); n != 1 {
		t.Fatalf("prune stale -> %d", n)
	}
	// delete
	s.pool.Exec(ctx, `DELETE FROM accounts WHERE email LIKE 'pgtest-%'`)
}
