package server

// Premium short subdomains (1-3 chars): priced add-on, gate before add,
// availability probe reports it, activation via the shared activate() path.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestShortDomainPricingMatrix(t *testing.T) {
	cases := []struct {
		dom            string
		crypto         bool
		want           int64
		wantPurchasble bool
	}{
		{"a", false, 300, true},
		{"ab", false, 200, true},
		{"abc", false, 100, true},
		{"abc", true, 200, true}, // crypto floor: $1 rounds up to the 2-char price
		{"abcd", false, 0, false},
		{"a", true, 300, true},
	}
	for _, c := range cases {
		got, ok := shortDomainPriceCents(c.dom, c.crypto)
		if ok != c.wantPurchasble || got != c.want {
			t.Errorf("shortDomainPriceCents(%q,crypto=%v) = (%d,%v), want (%d,%v)", c.dom, c.crypto, got, ok, c.want, c.wantPurchasble)
		}
	}
	if id := shortDomainProductID("ab"); id != "short-domain-2" {
		t.Fatalf("product id: %q", id)
	}
}

func TestShortDomainGateAndEntitlement(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	if _, err := s.activate("u@x.com", "starter"); err != nil {
		t.Fatal(err)
	}
	post := func(dom string) *httptest.ResponseRecorder {
		body := `{"email":"u@x.com","domain":"` + dom + `","remove":false}`
		r := httptest.NewRequest(http.MethodPost, "/me/domain", bytes.NewBufferString(body))
		r.AddCookie(s.sessionCookie("u@x.com"))
		rec := httptest.NewRecorder()
		s.handleMeAction(rec, r, "domain")
		return rec
	}

	// not purchased yet: 402 with the product and price
	rec := post("ab")
	if rec.Code != http.StatusPaymentRequired || !strings.Contains(rec.Body.String(), `"product":"short-domain-2"`) || !strings.Contains(rec.Body.String(), `"price_cents":200`) {
		t.Fatalf("gate: got %d %s", rec.Code, rec.Body.String())
	}
	// longer names are unaffected
	if rec := post("free-name"); rec.Code != http.StatusOK {
		t.Fatalf("long name: %d %s", rec.Code, rec.Body.String())
	}
	// activation (the same path every webhook calls) grants the entitlement
	if _, err := s.activate("u@x.com", "short-domain-2"); err != nil {
		t.Fatal(err)
	}
	if rec := post("ab"); rec.Code != http.StatusOK {
		t.Fatalf("after purchase: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post("a"); rec.Code != http.StatusPaymentRequired {
		t.Fatalf("1-char needs the bigger entitlement: %d", rec.Code)
	}
	// expired entitlement re-gates
	s.accounts.Mutate("u@x.com", func(a *Account) { a.PaidShort.ExpiresAt = time.Now().Add(-time.Hour) })
	if rec := post("cd"); rec.Code != http.StatusPaymentRequired {
		t.Fatalf("expired: %d", rec.Code)
	}
}

func TestShortDomainFreeAccountEntitlement(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	// a Free account buys the entitlement directly (no paid tier)
	key, err := s.activate("f@x.com", "short-domain-3")
	if err != nil || key == "" {
		t.Fatalf("free purchase: %v", err)
	}
	acct, ok := s.accounts.LookupEmail("f@x.com")
	if !ok || acct.Tier != "free" || acct.PaidShort == nil || acct.PaidShort.Len != 3 {
		t.Fatalf("entitlement wrong: %+v", acct)
	}
	// free tier has 0 custom domains, but the entitlement lifts the cap for one
	body := `{"email":"f@x.com","domain":"xyz","remove":false}`
	r := httptest.NewRequest(http.MethodPost, "/me/domain", bytes.NewBufferString(body))
	r.AddCookie(s.sessionCookie("f@x.com"))
	rec := httptest.NewRecorder()
	s.handleMeAction(rec, r, "domain")
	if rec.Code != http.StatusOK {
		t.Fatalf("free + entitlement add: %d %s", rec.Code, rec.Body.String())
	}
}

func TestShortDomainCheckReportsPremium(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	if _, err := s.activate("u@x.com", "pro"); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/me/domain-check?d=qq", nil)
	r.AddCookie(s.sessionCookie("u@x.com"))
	rec := httptest.NewRecorder()
	s.handleDomainCheck(rec, r)
	if !strings.Contains(rec.Body.String(), `"premium":true`) || !strings.Contains(rec.Body.String(), `"price_cents":200`) {
		t.Fatalf("probe: %s", rec.Body.String())
	}
	// entitled account sees a plain ok
	if _, err := s.activate("u@x.com", "short-domain-2"); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodGet, "/me/domain-check?d=qq", nil)
	r2.AddCookie(s.sessionCookie("u@x.com"))
	s.handleDomainCheck(rec, r2)
	if !strings.Contains(rec.Body.String(), `"ok":true`) || strings.Contains(rec.Body.String(), "premium") {
		t.Fatalf("entitled probe: %s", rec.Body.String())
	}
}
