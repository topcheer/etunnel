package server

// Regression guard for the mobile audit: /login and /me must declare a
// viewport meta, or phones render them at desktop width with shrunken text.
// Suggested by the team review; the pricing page is covered separately in
// payments_test.go (TestPricingPageMobileShape).

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPortalPagesDeclareViewport(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	if _, err := s.activate("m@x.com", "pro"); err != nil {
		t.Fatal(err)
	}

	// /me with a valid session: 200, viewport meta, and the subscription line
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.AddCookie(s.sessionCookie("m@x.com"))
	w := httptest.NewRecorder()
	s.handleMe(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("/me with a session: %d (want 200; a 303 means the cookie was rejected)", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `name="viewport"`) {
		t.Fatalf("/me is missing the viewport meta (%d bytes rendered)", w.Body.Len())
	}
	if !strings.Contains(body, "renews/expires:") {
		t.Fatalf("/me does not show the subscription expiry for a paid account")
	}

	// Unauthenticated /me must redirect: otherwise the assertion above could
	// pass on a page that never rendered.
	plain := httptest.NewRecorder()
	s.handleMe(plain, httptest.NewRequest(http.MethodGet, "/me", nil))
	if plain.Code != http.StatusSeeOther {
		t.Fatalf("unauthenticated /me: %d, want 303", plain.Code)
	}

	// /login renders without a session and must carry the meta too.
	lw := httptest.NewRecorder()
	s.handleLogin(lw, httptest.NewRequest(http.MethodGet, "/login", nil))
	lbody := lw.Body.String()
	if lw.Code != http.StatusOK {
		t.Fatalf("/login: %d", lw.Code)
	}
	if !strings.Contains(lbody, `name="viewport"`) {
		t.Fatalf("/login is missing the viewport meta (%d bytes rendered)", lw.Body.Len())
	}
}
