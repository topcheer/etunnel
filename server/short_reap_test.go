package server

// #95: expired PaidShort entitlements must be reclaimed by the tier reaper
// (with their 1-3 char names released); longer tier-quota domains survive.

import (
	"testing"
	"time"
)

func TestReapExpiredShortDomains(t *testing.T) {
	s := newBillingTestServer(t)
	now := time.Now()

	// expired entitlement, owns a short name + a regular custom domain
	if _, err := s.activate("a@x.com", "starter"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.activate("b@x.com", "starter"); err != nil {
		t.Fatal(err)
	}
	s.accounts.Mutate("a@x.com", func(a *Account) {
		a.Tier = "free"
		a.Domains = []string{"abc", "myapp"}
		a.PaidShort = &ShortDomain{Len: 3, ExpiresAt: now.Add(-time.Hour)}
	})
	// live entitlement must be untouched
	s.accounts.Mutate("b@x.com", func(a *Account) {
		a.Tier = "free"
		a.Domains = []string{"xy"}
		a.PaidShort = &ShortDomain{Len: 2, ExpiresAt: now.Add(time.Hour)}
	})

	s.reapExpiredTiers(now)

	a, _ := s.accounts.LookupEmail("a@x.com")
	if a.PaidShort != nil {
		t.Fatal("expired PaidShort not reclaimed")
	}
	if containsStr(a.Domains, "abc") {
		t.Fatalf("expired short name still registered: %v", a.Domains)
	}
	if !containsStr(a.Domains, "myapp") {
		t.Fatalf("tier-quota domain must survive: %v", a.Domains)
	}

	b, _ := s.accounts.LookupEmail("b@x.com")
	if b.PaidShort == nil || !containsStr(b.Domains, "xy") {
		t.Fatal("live entitlement was touched")
	}
}
