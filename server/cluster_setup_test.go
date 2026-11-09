package server

// The default (no env) wire-up must be exactly the single-node setup, and
// healthz must flip 503 when draining.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDefaultClusterIsSingleNode(t *testing.T) {
	t.Setenv("ETUNNEL_CLUSTER", "")
	cc, closers, err := setupCluster(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { for _, c := range closers { c() } }()
	if cc.Store != nil {
		t.Fatal("default must keep the JSON store")
	}
	if _, ok := cc.Counter.(*memCounter); !ok {
		t.Fatal("default must use the in-memory counter")
	}
	if cc.Router != nil {
		t.Fatal("default must have no router")
	}
}

func TestHealthzFlipsOnDrain(t *testing.T) {
	s := newBillingTestServer(t)
	h := s.HealthzHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("healthy: %d %q", rec.Code, rec.Body.String())
	}
	s.Drain()
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec2.Code != http.StatusServiceUnavailable || !strings.Contains(rec2.Body.String(), "draining") {
		t.Fatalf("draining: %d %q", rec2.Code, rec2.Body.String())
	}
}

func TestUnknownClusterModeFails(t *testing.T) {
	t.Setenv("ETUNNEL_CLUSTER", "banana")
	if _, _, err := setupCluster(t.Context()); err == nil || !strings.Contains(err.Error(), "banana") {
		t.Fatalf("err = %v", err)
	}
}
