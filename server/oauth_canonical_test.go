package server

// Auth flows must run on the service host: the OAuth state cookie is
// host-only, so a login that starts on the apex domain (etun.uk) and lands
// on the service host (t.etun.uk) loses the cookie and every provider fails
// with "bad oauth state". The handlers canonicalize with one redirect.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOAuthCanonicalHostRedirect(t *testing.T) {
	s := newBillingTestServer(t) // Domain: t.test
	s.auth.googleID = "x"

	// off-host (apex): 303 to the same path+query on the service host
	r := httptest.NewRequest(http.MethodGet, "/auth/google/callback?code=c&state=st", nil)
	r.Host = "etun.uk"
	rec := httptest.NewRecorder()
	s.handleOAuthCallback(rec, r, "google")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("apex callback: %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "https://t.test/auth/google/callback?code=c&state=st") {
		t.Fatalf("canonical target loses query: %q", loc)
	}

	// login start on apex: same canonicalization
	r = httptest.NewRequest(http.MethodGet, "/auth/google/login", nil)
	r.Host = "etun.uk"
	rec = httptest.NewRecorder()
	s.handleOAuthLogin(rec, r, "google")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "https://t.test/auth/google/login") {
		t.Fatalf("apex login: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if strings.Contains(rec.Header().Get("Set-Cookie"), "et_oauth_state") {
		t.Fatal("state cookie must not be set on the off-host hop")
	}

	// on the service host: flow proceeds untouched (303 to Google, state cookie set)
	r = httptest.NewRequest(http.MethodGet, "/auth/google/login", nil)
	r.Host = "t.test"
	rec = httptest.NewRecorder()
	s.handleOAuthLogin(rec, r, "google")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "https://accounts.google.com/") {
		t.Fatalf("service-host login: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), "et_oauth_state") {
		t.Fatal("service-host login must set the state cookie")
	}
}
