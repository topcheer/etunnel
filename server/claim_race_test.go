package server

// #101: two accounts claiming the same subdomain concurrently must not
// both win - TryClaimDomain decides uniquely under one critical section
// (the old lock-free subdomainTakenBy precheck raced a Mutate append).

import (
	"sync"
	"testing"
)

func TestTryClaimDomainConcurrentSingleWinner(t *testing.T) {
	s := newBillingTestServer(t)
	if _, err := s.activate("a@x.com", "starter"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.activate("b@x.com", "starter"); err != nil {
		t.Fatal(err)
	}

	const racers = 8
	var wg sync.WaitGroup
	wins := make([]bool, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// alternate between the two accounts, all racing for "gold"
			email := "a@x.com"
			if i%2 == 1 {
				email = "b@x.com"
			}
			wins[i] = s.accounts.TryClaimDomain(email, "gold", func(a *Account) bool {
				return len(a.Domains) < 10
			})
		}(i)
	}
	wg.Wait()

	total := 0
	for _, w := range wins {
		if w {
			total++
		}
	}
	// idempotent re-adds on the SAME account also return true: the invariant
	// is at most one winning ACCOUNT (a real fight) and the name never on
	// both. 4 wins here = 1 original + re-adds on that one account.
	if total < 1 || total > racers {
		t.Fatalf("wins out of range: %d", total)
	}

	// the name must live on exactly one account
	a, _ := s.accounts.LookupEmail("a@x.com")
	b, _ := s.accounts.LookupEmail("b@x.com")
	onA, onB := containsStr(a.Domains, "gold"), containsStr(b.Domains, "gold")
	if onA == onB {
		t.Fatalf("gold must be on exactly one account: a=%v b=%v", onA, onB)
	}
}
