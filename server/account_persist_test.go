package server

// #93: persistence failures were silently discarded (`_ = saveLocked()`)
// across Delete/ResetKey/CancelSubscription/Mutate; the store now reports
// every failure through the injected onSaveErr hook.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAccountStoreReportsSaveFailures(t *testing.T) {
	// path under a regular FILE (not a dir): MkdirAll fails -> save error
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("i am a file"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(blocker, "sub", "accounts.json")

	s := &AccountStore{path: bad, byKey: map[string]*Account{}, byMail: map[string]*Account{}}
	var got error
	s.onSaveErr = func(err error) { got = err }

	s.byMail["u@x.com"] = &Account{APIKey: "k", Email: "u@x.com", Tier: "free"}
	if ok := s.Mutate("u@x.com", func(a *Account) { a.Tier = "starter" }); !ok {
		t.Fatal("mutate should succeed in-memory")
	}
	if got == nil {
		t.Fatal("persistence failure was not reported through onSaveErr")
	}

	// and the in-memory mutation is still visible (documented split)
	if a := s.byMail["u@x.com"]; a.Tier != "starter" {
		t.Fatalf("memory mutation lost: %s", a.Tier)
	}
}
