package server

// The legal pages must exist, be reachable and actually state the things a
// GDPR/CCPA reader looks for. Cheap guard against a refactor dropping them.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegalPages(t *testing.T) {
	s := newBillingTestServer(t)
	cases := map[string]struct {
		handle func(http.ResponseWriter, *http.Request)
		want   []string
	}{
		"/terms": {
			handle: s.handleTerms,
			want: []string{"Terms of Service", "Acceptable use",
				"Payments are processed by third parties", "info@"},
		},
		"/privacy": {
			handle: s.handlePrivacy,
			want: []string{"Privacy Policy", "What we collect", "Your rights",
				"we do not sell", "info@"},
		},
	}
	for path, c := range cases {
		rec := httptest.NewRecorder()
		c.handle(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		body := strings.ToLower(rec.Body.String())
		if !strings.Contains(body, `name="viewport"`) {
			t.Errorf("%s is missing the viewport meta", path)
		}
		for _, w := range c.want {
			if !strings.Contains(body, strings.ToLower(w)) {
				t.Errorf("%s is missing %q", path, w)
			}
		}
	}
}

// Both pages must be linked from the site chrome, or they are effectively
// unreachable for compliance purposes.
func TestLegalPagesLinked(t *testing.T) {
	s := newBillingTestServer(t)
	for _, path := range []string{"/pricing", "/login"} {
		rec := httptest.NewRecorder()
		switch path {
		case "/pricing":
			s.handlePricing(rec, httptest.NewRequest(http.MethodGet, path, nil))
		case "/login":
			s.handleLogin(rec, httptest.NewRequest(http.MethodGet, path, nil))
		}
		body := rec.Body.String()
		for _, link := range []string{`href="/terms"`, `href="/privacy"`} {
			if !strings.Contains(body, link) {
				t.Errorf("%s does not link %s", path, link)
			}
		}
	}
}
