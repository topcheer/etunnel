package server

// Subdomain availability: 1-63 chars allowed, debounced inline check on
// /me, cross-account duplicates rejected on both check and submit.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDomainCheckEndpoint(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	if _, err := s.activate("a@x.com", "pro"); err != nil {
		t.Fatal(err)
	}
	s.accounts.Mutate("a@x.com", func(a *Account) { a.Domains = []string{"taken-sub"} })
	// premium gate: 3-char names need the entitlement before the dup probe
	if _, err := s.activate("a@x.com", "short-domain-3"); err != nil {
		t.Fatal(err)
	}

	get := func(q string, cookieEmail string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/me/domain-check"+q, nil)
		if cookieEmail != "" {
			r.AddCookie(s.sessionCookie(cookieEmail))
		}
		rec := httptest.NewRecorder()
		s.handleDomainCheck(rec, r)
		return rec
	}

	// anonymous: 401
	if rec := get("?d=x", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon: %d", rec.Code)
	}
	// taken by another account
	rec := get("?d=taken-sub", "a@x.com")
	// self-owned is not "taken" for the owner; probe from a second account
	if _, err := s.activate("b@x.com", "pro"); err != nil {
		t.Fatal(err)
	}
	rec = get("?d=taken-sub", "b@x.com")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":false`) || !strings.Contains(rec.Body.String(), "taken") {
		t.Fatalf("taken: %d %s", rec.Code, rec.Body.String())
	}
	// own subdomain reads available to its owner (re-add is idempotent)
	if rec := get("?d=taken-sub", "a@x.com"); !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("own domain must read ok for owner: %s", rec.Body.String())
	}
	// free
	if rec := get("?d=fresh", "b@x.com"); !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("free: %s", rec.Body.String())
	}
	// pasted hostname normalizes on the check path too
	if rec := get("?d=fresh.t.etun.uk", "b@x.com"); !strings.Contains(rec.Body.String(), `"domain":"fresh"`) {
		t.Fatalf("normalize: %s", rec.Body.String())
	}
	// invalid input: 400 + reason
	if rec := get("?d=", "b@x.com"); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty: %d", rec.Code)
	}
}

func TestDomainAddRejectsCrossAccountDuplicate(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	for _, e := range []string{"a@x.com", "b@x.com"} {
		if _, err := s.activate(e, "pro"); err != nil {
			t.Fatal(err)
		}
	}
	// "dup" is a 3-char premium name: grant the buyer the entitlement
	if _, err := s.activate("a@x.com", "short-domain-3"); err != nil {
		t.Fatal(err)
	}
	body := `{"email":"a@x.com","domain":"dup","remove":false}`
	r := httptest.NewRequest(http.MethodPost, "/me/domain", bytes.NewBufferString(body))
	r.AddCookie(s.sessionCookie("a@x.com"))
	rec := httptest.NewRecorder()
	s.handleMeAction(rec, r, "domain")
	if rec.Code != http.StatusOK {
		t.Fatalf("first add: %d %s", rec.Code, rec.Body.String())
	}
	// second account, same name: 409 now (was silently allowed)
	r = httptest.NewRequest(http.MethodPost, "/me/domain", bytes.NewBufferString(`{"email":"b@x.com","domain":"dup","remove":false}`))
	r.AddCookie(s.sessionCookie("b@x.com"))
	rec = httptest.NewRecorder()
	s.handleMeAction(rec, r, "domain")
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate: got %d %s, want 409", rec.Code, rec.Body.String())
	}
	// single character is premium like all 1-3 char names: 402 without the entitlement
	r = httptest.NewRequest(http.MethodPost, "/me/domain", bytes.NewBufferString(`{"email":"b@x.com","domain":"z","remove":false}`))
	r.AddCookie(s.sessionCookie("b@x.com"))
	rec = httptest.NewRecorder()
	s.handleMeAction(rec, r, "domain")
	if rec.Code != http.StatusPaymentRequired || !strings.Contains(rec.Body.String(), "short-domain-1") {
		t.Fatalf("1-char: got %d %s", rec.Code, rec.Body.String())
	}
}
