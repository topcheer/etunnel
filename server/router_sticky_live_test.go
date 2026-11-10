package server

// Live test against the Redis on host 13 (db 15). Skipped when unreachable.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStickyRouterLive(t *testing.T) {
	url := os.Getenv("ET_REDIS_TEST_URL")
	if url == "" {
		t.Skip("ET_REDIS_TEST_URL not set (live sticky-router test)")
	}
	a, err := newStickyRouter(url, "10.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	b, err := newStickyRouter(url, "10.0.0.2:8080")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(); b.Close() })

	// a claims; b must see a as the owner via the table
	a.Claim("sticky-test-sub")
	if got := b.Lookup("sticky-test-sub"); got != "10.0.0.1:8080" {
		t.Fatalf("b.Lookup -> %q", got)
	}
	// a resolves itself as owner ("")
	if got := a.Lookup("sticky-test-sub"); got != "" {
		t.Fatalf("a.Lookup -> %q", got)
	}
	// b forwards to a (origin played by httptest)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "origin-hello fwd=%s", r.Header.Get("X-Etunnel-Forward"))
	}))
	defer origin.Close()
	// point b's lookup at the origin by rewriting the route (same trick as
	// the mesh test): claim from the "origin"
	a.rdb.Set(a.ctx0, "et:route:sticky-test-sub", strings.TrimPrefix(origin.URL, "http://"), time.Minute)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://x/", nil)
	if !b.Forward("sticky-test-sub", rec, req) {
		t.Fatal("Forward false")
	}
	if !strings.Contains(rec.Body.String(), "origin-hello") {
		t.Fatalf("body %q", rec.Body.String())
	}
	// loop guard
	req2 := httptest.NewRequest(http.MethodGet, "http://x/", nil)
	req2.Header.Set("X-Etunnel-Forward", "x")
	if b.Forward("sticky-test-sub", httptest.NewRecorder(), req2) {
		t.Fatal("loop guard failed")
	}
	// release clears for both
	a.Release("sticky-test-sub")
	if got := b.Lookup("sticky-test-sub"); got != "" {
		t.Fatalf("after release -> %q", got)
	}
}
