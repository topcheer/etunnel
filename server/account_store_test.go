package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Issue #18: a torn/corrupt accounts file must not brick the daemon, and stale
// free accounts must be prunable by activity.

func TestNewAccountStoreRecoversFromCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.json")
	const garbage = `[{"apiKey": "et_trunc`
	if err := os.WriteFile(path, []byte(garbage), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := NewAccountStore(path)
	if err != nil {
		t.Fatalf("corrupt file refused startup: %v", err)
	}
	if len(store.byKey) != 0 {
		t.Fatal("store should start empty")
	}

	matches, _ := filepath.Glob(path + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("expected the damaged file to be preserved as %s.corrupt-*, found %v", path, matches)
	}
	saved, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != garbage {
		t.Fatalf("preserved bytes differ: %q", string(saved))
	}

	// The daemon keeps working: a fresh save writes a valid file again.
	if _, err := store.Create("new@x.com", "free"); err != nil {
		t.Fatalf("store unusable after recovery: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("save after recovery failed: %v", err)
	}
	if !strings.Contains(string(b), "new@x.com") {
		t.Fatalf("rebuilt file missing the new account: %s", b)
	}
}

func TestPruneInactiveOnlyFreeStaleAccounts(t *testing.T) {
	old := time.Now().Add(-90 * 24 * time.Hour)
	recent := time.Now().Add(-time.Hour)

	mk := func(email, tier string, created, seen time.Time, domains ...string) *Account {
		return &Account{APIKey: "et_" + email, Email: email, Tier: tier, Created: created, LastSeen: seen, Domains: domains}
	}
	staleFree := mk("stale-free@x.com", "free", old, old)
	neverSeen := mk("never-seen@x.com", "free", old, time.Time{}) // falls back to Created
	recentFree := mk("recent@x.com", "free", old, recent)
	paidOld := mk("paid@x.com", "pro", old, old)
	freeWithDomain := mk("domain@x.com", "free", old, old, "app")

	s := &AccountStore{byKey: map[string]*Account{}, byMail: map[string]*Account{}}
	for _, a := range []*Account{staleFree, neverSeen, recentFree, paidOld, freeWithDomain} {
		s.byKey[a.APIKey] = a
		s.byMail[a.Email] = a
	}

	got := s.PruneInactive(time.Now().Add(-30*24*time.Hour), "free")
	if got != 2 {
		t.Fatalf("pruned %d accounts, want 2 (stale free + never-seen)", got)
	}
	for _, gone := range []*Account{staleFree, neverSeen} {
		if _, ok := s.byKey[gone.APIKey]; ok {
			t.Fatalf("%s should have been pruned", gone.Email)
		}
		if _, ok := s.byMail[gone.Email]; ok {
			t.Fatalf("email index still holds %s", gone.Email)
		}
	}
	for _, kept := range []*Account{recentFree, paidOld, freeWithDomain} {
		if _, ok := s.byKey[kept.APIKey]; !ok {
			t.Fatalf("%s must be kept (recent, paid, or holds a custom domain)", kept.Email)
		}
	}
}

// Pruning relies on an activity stamp, so Snapshot must record one.
func TestSnapshotStampsLastSeen(t *testing.T) {
	store, err := NewAccountStore("")
	if err != nil {
		t.Fatal(err)
	}
	acct, err := store.Create("touch@x.com", "free")
	if err != nil {
		t.Fatal(err)
	}
	if !acct.LastSeen.IsZero() {
		t.Fatal("fresh account should have no activity stamp")
	}
	snap, ok := store.Snapshot(acct.APIKey)
	if !ok {
		t.Fatal("snapshot failed")
	}
	if snap.LastSeen.IsZero() {
		t.Fatal("Snapshot did not stamp LastSeen; pruning would see the account as stale")
	}
}
