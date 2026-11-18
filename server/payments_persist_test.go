// payments_persist_test.go - activation must be durable, and a failed write must
// not be reported as success (#218).
//
// #93 made persistence failures visible in the log. #218 is about the caller:
// the payment path answered 200 (so the provider stops retrying) while the tier
// existed only in memory, and the next store reload dropped it.
package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The regression that matters most: after activate(), a *fresh* store loaded from
// the same file must see the new tier. This also proves that removing the
// redundant saveLockedSafe() calls did not silently stop the write.
func TestActivateIsDurableAcrossStoreReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.json")
	cfg := Config{
		Domain:       "t.test",
		Addr:         "127.0.0.1:1",
		TLS:          TLSConfig{Mode: TLSModeHTTP},
		AccountsFile: path,
		Tiers:        DefaultTiers(),
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	key, err := s.activate("durable@x.com", "pro")
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if key == "" {
		t.Fatal("no API key")
	}

	// Simulate a restart: brand-new store over the same file.
	reloaded, err := NewAccountStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	acct, ok := reloaded.LookupEmail("durable@x.com")
	if !ok {
		t.Fatal("account vanished after reload - activate did not persist")
	}
	if acct.Tier != "pro" {
		t.Fatalf("tier after reload = %q, want pro", acct.Tier)
	}
	if acct.ExpiresAt.IsZero() {
		t.Fatal("expiry not persisted")
	}

	// And the short-domain product, which took the other (previously swallowed)
	// persistence path.
	if _, err := s.activate("short@x.com", "short-domain-1"); err != nil {
		t.Fatalf("activateShortDomain: %v", err)
	}
	reloaded2, err := NewAccountStore(path)
	if err != nil {
		t.Fatalf("reload2: %v", err)
	}
	a2, ok := reloaded2.LookupEmail("short@x.com")
	if !ok {
		t.Fatal("short-domain account vanished after reload")
	}
	if a2.PaidShort == nil || a2.PaidShort.Len != 1 {
		t.Fatalf("PaidShort not persisted: %+v", a2.PaidShort)
	}
}

// A write that cannot reach disk must surface as an error, so the webhook can be
// answered with 5xx and the provider retries.
//
// The store is set up healthy and then the directory is made unwritable: building
// it over an already-bad path just makes New() refuse the config, which would
// skip this test instead of proving anything.
func TestActivateSurfacesPersistFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions are not enforced")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.json")
	s, err := New(Config{
		Domain:       "t.test",
		Addr:         "127.0.0.1:1",
		TLS:          TLSConfig{Mode: TLSModeHTTP},
		AccountsFile: path,
		Tiers:        DefaultTiers(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Create the account while the store can still write.
	if _, err := s.activate("nope@x.com", "pro"); err != nil {
		t.Fatalf("seed activate: %v", err)
	}

	// Now make every write inside the dir fail.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	// A VALID tier, so the call reaches the renewal branch: an unknown tier would
	// return an error for the wrong reason and make this test pass vacuously
	// (that is exactly how the first draft of this test was broken).
	_, err = s.activate("nope@x.com", "pro")
	if err == nil {
		t.Fatal("activate reported success although the tier could not be persisted")
	}
	if !strings.Contains(err.Error(), "persist") {
		t.Fatalf("error came from the wrong place: %v", err)
	}
}

// mutate() must distinguish "no such account" from "found but not durable": the
// first is a no-op, the second is an error.
func TestMutateReportsDurabilitySeparately(t *testing.T) {
	dir := t.TempDir()
	store, err := NewAccountStore(filepath.Join(dir, "a.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("m@x.com", "free"); err != nil {
		t.Fatal(err)
	}

	if found, perr := store.mutate("m@x.com", func(a *Account) { a.Tier = "pro" }); !found || perr != nil {
		t.Fatalf("happy path: found=%v err=%v", found, perr)
	}
	if found, perr := store.mutate("absent@x.com", func(a *Account) {}); found || perr != nil {
		t.Fatalf("absent account: found=%v err=%v, want false/nil", found, perr)
	}
}
