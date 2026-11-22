package server

// #336 regressions: dead-session replacement must hand the replaced
// holder's counters back (HIGH), and the passkeyBegins table has a hard
// cap that evicts the oldest window even when every entry is fresh (LOW2).

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// #336 HIGH timing case: holder closes -> a replacement AcquireBounded
// takes the name -> the old holder's Release arrives late. The counters
// must end at zero (not -1) and the account must keep its quota.
func TestReplacementReleasesReplacedHolderCounters(t *testing.T) {
	r := newRegistry()
	acct := &Account{APIKey: "d1"}
	old := &Session{id: "old", sub: "svc", account: acct, custom: true, closed: make(chan struct{})}
	if err := r.AcquireBounded("svc", old, acct, 1, true, 1); err != nil {
		t.Fatalf("initial acquire: %v", err)
	}
	if got := r.acctAll[acct.APIKey]; got != 1 {
		t.Fatalf("counter after initial acquire = %d, want 1", got)
	}

	// Holder's socket closes (close(closed)) ...
	close(old.closed)
	// ... then the supervisor's replacement acquire replaces it.
	rep := &Session{id: "rep", sub: "svc", account: acct, custom: true, closed: make(chan struct{})}
	if err := r.AcquireBounded("svc", rep, acct, 1, true, 1); err != nil {
		t.Fatalf("replacement acquire: %v", err)
	}
	// The replacement holds exactly one slot.
	if got := r.acctAll[acct.APIKey]; got != 1 {
		t.Fatalf("counter after replacement = %d, want 1 (#336 HIGH)", got)
	}

	// The old holder's Release arrives late: it must be a no-op (the
	// cur==s guard swallows it) and NOT decrement the replacement's count.
	r.Release("svc", old)
	if got := r.acctAll[acct.APIKey]; got != 1 {
		t.Fatalf("late old-holder release drifted the counter to %d, want 1 (#336 HIGH)", got)
	}

	// Counters stay truthful end to end: release the replacement and the
	// account is back to zero - a fresh acquire succeeds (no lockout).
	r.Release("svc", rep)
	if got := r.acctAll[acct.APIKey] + r.acctCustom[acct.APIKey]; got != 0 {
		t.Fatalf("counters after final release = all:%d custom:%d, want 0 (#336 HIGH)",
			r.acctAll[acct.APIKey], r.acctCustom[acct.APIKey])
	}
	fresh := &Session{id: "f", sub: "svc2", account: acct, custom: true, closed: make(chan struct{})}
	if err := r.AcquireBounded("svc2", fresh, acct, 1, true, 1); err != nil {
		t.Fatalf("account locked out after drift-free cycle: %v (#336 HIGH)", err)
	}
}

// #331 LOW5 consistency guard on the same path: replacement keeps the
// custom flag true so custom counters cannot drift either.
func TestReplacementKeepsCustomCounters(t *testing.T) {
	r := newRegistry()
	acct := &Account{APIKey: "d2"}
	old := &Session{id: "old", sub: "svc", account: acct, custom: true, closed: make(chan struct{})}
	if err := r.AcquireBounded("svc", old, acct, 0, true, 1); err != nil {
		t.Fatal(err)
	}
	close(old.closed)
	rep := &Session{id: "rep", sub: "svc", account: acct, custom: true, closed: make(chan struct{})}
	if err := r.AcquireBounded("svc", rep, acct, 0, true, 1); err != nil {
		t.Fatal(err)
	}
	// A different custom name for the same account must now be quota-blocked
	// (custom count == 1 == limit), proving the replacement counted once.
	other := &Session{id: "o", sub: "other", account: acct, custom: true, closed: make(chan struct{})}
	if err := r.AcquireBounded("other", other, acct, 0, true, 1); !errors.Is(err, ErrAccountQuota) {
		t.Fatalf("custom counter drifted after replacement: %v (#336 HIGH)", err)
	}
	// A non-custom name is still bounded only by MaxTunnels (0 = unlimited
	// here), so it succeeds.
	plain := &Session{id: "p", sub: "plain", account: acct, closed: make(chan struct{})}
	if err := r.AcquireBounded("plain", plain, acct, 0, false, 0); err != nil {
		t.Fatalf("non-custom acquire blocked: %v", err)
	}
}

// #336 LOW2: filling the passkeyBegins table with 4096+ FRESH windows must
// not grow it past the hard cap - the oldest windows are evicted.
func TestPasskeyBeginsHardCap(t *testing.T) {
	s := newBillingTestServer(t)
	// Seed the cap with fresh windows (no prune would remove these).
	for i := 0; i < 4096; i++ {
		s.passkeyBegins[fmt.Sprintf("10.0.%d.%d:1", i/250, i%250)] =
			&passkeyBeginRate{windowStart: time.Now(), count: 1}
	}
	before := len(s.passkeyBegins)
	// A brand-new IP must still be admitted: the cap evicts the oldest.
	if !s.allowPasskeyBegin("9.9.9.9:1") {
		t.Fatal("hard cap locked out a brand-new IP (#336 LOW2)")
	}
	if len(s.passkeyBegins) > 4096 {
		t.Fatalf("table grew past the hard cap: %d entries (#336 LOW2)", len(s.passkeyBegins))
	}
	if _, ok := s.passkeyBegins["9.9.9.9:1"]; !ok {
		t.Fatal("new IP was not recorded")
	}
	_ = before
}
