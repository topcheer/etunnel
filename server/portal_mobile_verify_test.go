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

// The account header must read cleanly for accounts without an OAuth provider
// (register/checkout-created): "Signed in as x@y" with no dangling "via".
func TestAccountHeaderWithoutProvider(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	if _, err := s.activate("noprov@x.com", "pro"); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/me", nil)
	r.AddCookie(s.sessionCookie("noprov@x.com"))
	rec := httptest.NewRecorder()
	s.handleMe(rec, r)
	body := rec.Body.String()
	if strings.Contains(body, "via <") || strings.Contains(body, "</b> via ") {
		t.Fatalf("dangling 'via' in the account header: %s", body[:200])
	}
	if !strings.Contains(body, "Signed in as") {
		t.Fatal("account header missing")
	}
}

// The favicon data URI must survive URL parsing: unencoded '#' truncates the
// SVG at the first color, leaving browsers with a broken icon.
func TestFaviconEncodesFragments(t *testing.T) {
	if strings.Contains(portalFavicon, "fill='#") || strings.Contains(portalFavicon, "stroke='#") {
		t.Fatal("favicon contains raw # - browsers treat it as a fragment and the icon breaks")
	}
	for _, part := range []string{"%230d1117", "%23e6edf3", "%233fb950"} {
		if !strings.Contains(portalFavicon, part) {
			t.Fatalf("expected encoded color %s in the favicon", part)
		}
	}
}
