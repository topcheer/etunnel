package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Pentest #37 (L1): the 80→443 redirect must not reflect an arbitrary Host
// header into the Location.
func TestRedirectHandlerHostValidation(t *testing.T) {
	s := newBillingTestServer(t) // Domain: t.test
	h := s.redirectHandler(nil)

	// Foreign host: no redirect, no reflection.
	r := httptest.NewRequest(http.MethodGet, "http://104.168.30.122/assets/x", nil)
	r.Host = "evil.example"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("foreign host: status %d, want 421", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Fatalf("foreign host: Location %q must not be set", loc)
	}

	// Base host: redirect to the canonical domain, not to the request's Host.
	r = httptest.NewRequest(http.MethodGet, "http://t.test/pricing", nil)
	r.Host = "www.t.test"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("base host: status %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://t.test/pricing" {
		t.Fatalf("base host: Location %q, want canonical https://t.test/pricing", loc)
	}

	// Tunnel subdomain: ours, so it redirects - keeping its own host.
	r = httptest.NewRequest(http.MethodGet, "http://user42.t.test/x?y=1", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("sub host: status %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://user42.t.test/x?y=1" {
		t.Fatalf("sub host: Location %q", loc)
	}

	// ACME HTTP-01 challenge answers for any host, before validation.
	served := false
	ch := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { served = true })
	hc := s.redirectHandler(ch)
	r = httptest.NewRequest(http.MethodGet, "http://evil.example/.well-known/acme-challenge/token", nil)
	r.Host = "evil.example"
	rec = httptest.NewRecorder()
	hc.ServeHTTP(rec, r)
	if !served {
		t.Fatal("ACME challenge must bypass host validation")
	}
}
