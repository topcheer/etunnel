package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Issue #13: etunneld is the network edge, so caller-supplied forwarding
// headers are untrusted. They must be replaced, never extended.

func TestForwardedHeadersAreAuthoritative(t *testing.T) {
	for _, isUpgrade := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodGet, "https://dev.t.test/", nil)
		req.RemoteAddr = "203.0.113.9:5555"
		req.Host = "dev.t.test"
		req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
		req.Header.Set("X-Real-Ip", "1.2.3.4")
		req.Header.Set("X-Forwarded-Host", "evil.example")
		req.Header.Set("X-Forwarded-Proto", "https")

		h := forwardRequestHeaders(req, isUpgrade)

		assertHeader(t, h, "X-Forwarded-For", "203.0.113.9", isUpgrade)
		assertHeader(t, h, "X-Real-Ip", "203.0.113.9", isUpgrade)
		assertHeader(t, h, "X-Forwarded-Host", "dev.t.test", isUpgrade)
	}
}

// A request with no forwarding headers still gets a single authoritative hop.
func TestForwardedHeadersSynthesised(t *testing.T) {
	// Plain http on purpose: this case asserts X-Forwarded-Proto reports "http"
	// for the TLS-less listener that only redirects/terminates ACME.
	req := httptest.NewRequest(http.MethodGet, "http://dev.t.test/", nil)
	req.RemoteAddr = "198.51.100.7:1234"
	req.Host = "dev.t.test"
	h := forwardRequestHeaders(req, false)
	assertHeader(t, h, "X-Forwarded-For", "198.51.100.7", false)
	assertHeader(t, h, "X-Real-Ip", "198.51.100.7", false)
	assertHeader(t, h, "X-Forwarded-Host", "dev.t.test", false)
	assertHeader(t, h, "X-Forwarded-Proto", "http", false)
}

func assertHeader(t *testing.T, h map[string][]string, name, want string, isUpgrade bool) {
	t.Helper()
	got := h[name]
	if len(got) != 1 || got[0] != want {
		t.Fatalf("upgrade=%v %s = %v, want exactly [%q]", isUpgrade, name, got, want)
	}
}
