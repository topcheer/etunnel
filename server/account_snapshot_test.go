package server

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newSnapshotTestStore(t *testing.T) *AccountStore {
	t.Helper()
	st, err := NewAccountStore(filepath.Join(t.TempDir(), "a.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Create("snap@x.com", "free"); err != nil {
		t.Fatal(err)
	}
	return st
}

// Issue #4: a snapshot must be a value the caller owns - later mutations of
// the live account must not be visible through it, and it must not alias the
// Domains slice.
func TestAccountSnapshotIsolated(t *testing.T) {
	st := newSnapshotTestStore(t)
	acct, ok := st.LookupEmail("snap@x.com")
	if !ok {
		t.Fatal("account missing")
	}
	st.Mutate("snap@x.com", func(a *Account) { a.Domains = []string{"dev"} })

	snap, ok := st.Snapshot(acct.APIKey)
	if !ok {
		t.Fatal("snapshot failed")
	}
	if snap.Tier != "free" || len(snap.Domains) != 1 || snap.Domains[0] != "dev" {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}

	st.Mutate("snap@x.com", func(a *Account) {
		a.Tier = "pro"
		a.Domains = append(a.Domains, "prod")
	})
	if snap.Tier != "free" || len(snap.Domains) != 1 {
		t.Fatalf("snapshot aliased live account: %+v", snap)
	}
	if _, ok := st.Snapshot("et_missing"); ok {
		t.Fatal("unknown key must not produce a snapshot")
	}
}

// Issue #4: the handshake read pattern (snapshot, then read fields/scan the
// domain list) must not race concurrent Mutate writers. Run with -race.
func TestAccountSnapshotConcurrentWithMutate(t *testing.T) {
	st := newSnapshotTestStore(t)
	live, _ := st.LookupEmail("snap@x.com")
	key := live.APIKey

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() { // billing/OAuth writer
		defer wg.Done()
		for i := 0; i < 300; i++ {
			st.Mutate("snap@x.com", func(a *Account) {
				if i%2 == 0 {
					a.Tier = "pro"
				} else {
					a.Tier = "free"
				}
				a.Domains = []string{fmt.Sprintf("d%d", i)}
			})
		}
		close(stop)
	}()

	wg.Add(1)
	go func() { // session handshake reader
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			snap, ok := st.Snapshot(key)
			if !ok {
				continue
			}
			_ = snap.Tier
			_ = snap.Email
			_ = containsStr(snap.Domains, "d1") // same call the handshake makes
		}
	}()
	wg.Wait()

	if got := strings.TrimSpace(key); got == "" {
		t.Fatal("empty API key")
	}
}
