package server

// The OAuth state cookie's SameSite attribute must match the provider's
// callback transport: Lax for redirect flows (immune to third-party-cookie
// blocking on the cross-site top-level GET), None only for Apple's
// form_post cross-site POST.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStateCookieSameSitePerProvider(t *testing.T) {
	s := newBillingTestServer(t)
	cases := map[string]string{
		"google": "SameSite=Lax",
		"github": "SameSite=Lax",
		"gitlab": "SameSite=Lax",
		"apple":  "SameSite=None",
	}
	for provider, want := range cases {
		w := httptest.NewRecorder()
		s.setStateCookie(w, provider)
		h := w.Header().Get("Set-Cookie")
		if !strings.Contains(h, want) || !strings.Contains(h, "Secure") || !strings.Contains(h, "HttpOnly") {
			t.Errorf("%s: cookie %q must carry %s; Secure; HttpOnly", provider, h, want)
		}
	}
	// the /login redirect itself carries the Lax cookie for google
	s.auth.googleID = "x"
	r := httptest.NewRequest(http.MethodGet, "/auth/google/login", nil)
	rec := httptest.NewRecorder()
	s.handleOAuthLogin(rec, r, "google")
	sc := rec.Header().Get("Set-Cookie")
	if !strings.Contains(sc, "SameSite=Lax") {
		t.Fatalf("google login sets %q, want SameSite=Lax", sc)
	}
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login redirect: %d", rec.Code)
	}
}
