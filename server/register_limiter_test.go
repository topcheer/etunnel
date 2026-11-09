package server

import (
	"net/http"
	"testing"
)

// Issue #17 (test isolation): the register throttle used to be process-global,
// so a second run of the same test in one process - or a second Server in the
// same process - got 429 and looked like a product failure. Each Server now
// owns its bucket.
func TestRegisterLimiterIsPerServer(t *testing.T) {
	const ip = "10.77.0.9"

	first := newBillingTestServer(t)
	if rec := doRegister(first, ip, `{"email":"per-server@x.com"}`); rec.Code != http.StatusOK {
		t.Fatalf("first server: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	second := newBillingTestServer(t)
	if rec := doRegister(second, ip, `{"email":"per-server@x.com"}`); rec.Code != http.StatusOK {
		t.Fatalf("second server shares the first server's bucket: got %d (%s)", rec.Code, rec.Body.String())
	}

	// Within one server the throttle still applies.
	if rec := doRegister(first, ip, `{"email":"per-server2@x.com"}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("same server, same IP: want 429, got %d", rec.Code)
	}
}

// The throttle must still fire inside a single server, and per IP.
func TestRegisterLimiterThrottlesWithinServer(t *testing.T) {
	s := newBillingTestServer(t)
	if rec := doRegister(s, "10.77.1.1", `{"email":"a1@x.com"}`); rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if rec := doRegister(s, "10.77.1.1", `{"email":"a2@x.com"}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", rec.Code)
	}
	// A different IP is unaffected.
	if rec := doRegister(s, "10.77.1.2", `{"email":"a3@x.com"}`); rec.Code != http.StatusOK {
		t.Fatalf("different IP: want 200, got %d", rec.Code)
	}
}
