package server

// Issue #38: /assets/ must never list the bundle directory.
// Issue #42: base-domain requests get one access-log line each, and admin
// auth failures are logged in full.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type logCapture struct {
	mu    sync.Mutex
	lines []string
}

func (l *logCapture) add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
}

func (l *logCapture) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	cap := &logCapture{}
	old := logPrintf
	logPrintf = func(f string, a ...any) (int, error) {
		cap.add(fmt.Sprintf(f, a...))
		return 0, nil
	}
	t.Cleanup(func() { logPrintf = old })
	return cap
}

func obsTestServer(t *testing.T, mut func(*Config)) *Server {
	t.Helper()
	cfg := Config{
		Domain:       "t.test",
		WSPath:       "/tunnel",
		TLS:          TLSConfig{Mode: TLSModeHTTP},
		AccountsFile: filepath.Join(t.TempDir(), "a.json"),
		Tiers:        DefaultTiers(),
	}
	if mut != nil {
		mut(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAssetsNoDirectoryListing(t *testing.T) {
	// The brand file server only mounts when an assets dir exists next to
	// the process; give it one in a scratch cwd.
	dir := t.TempDir()
	assets := filepath.Join(dir, "assets")
	if err := os.MkdirAll(assets, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "hero-loop.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir) // restored automatically at test end

	s := obsTestServer(t, nil)

	// Directory index request: 404, never the file listing.
	req := httptest.NewRequest(http.MethodGet, "/assets/", nil)
	req.Host = "t.test"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /assets/: want 404, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "hero-loop") {
		t.Fatal("directory listing leaked file names")
	}

	// A concrete file still serves.
	req2 := httptest.NewRequest(http.MethodGet, "/assets/hero-loop.mp4", nil)
	req2.Host = "t.test"
	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET /assets/hero-loop.mp4: want 200, got %d", rec2.Code)
	}
}

func TestAccessLogBaseDomainOnly(t *testing.T) {
	cap := captureLogs(t)
	s := obsTestServer(t, nil)
	h := s.Handler()

	// Base-domain request: exactly one access line with method/path/status.
	req := httptest.NewRequest(http.MethodGet, "/pricing", nil)
	req.Host = "t.test"
	h.ServeHTTP(httptest.NewRecorder(), req)
	var hit int
	for _, l := range cap.all() {
		if strings.HasPrefix(l, "etunneld: http: GET /pricing 200 ") {
			hit++
		}
	}
	if hit != 1 {
		t.Fatalf("access line for base domain: got %d, want 1 (logs: %v)", hit, cap.all())
	}

	// Tunnel-subdomain request: the proxy hot path stays silent.
	before := len(cap.all())
	req2 := httptest.NewRequest(http.MethodGet, "/whatever", nil)
	req2.Host = "some-tunnel.t.test"
	h.ServeHTTP(httptest.NewRecorder(), req2)
	if got := len(cap.all()) - before; got != 0 {
		t.Fatalf("tunnel host logged %d lines, want 0", got)
	}
}

func TestAdminAuthFailureLogged(t *testing.T) {
	cap := captureLogs(t)
	s := obsTestServer(t, func(c *Config) { c.AdminToken = "real-token" })

	for _, path := range []string{"/admin/accounts", "/stats"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Admin-Token", "wrong")
		rec := httptest.NewRecorder()
		s.adminHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: want 401, got %d", path, rec.Code)
		}
	}
	logs := cap.all()
	found := 0
	for _, l := range logs {
		if strings.Contains(l, "admin: auth FAILED GET /admin/accounts ") ||
			strings.Contains(l, "admin: auth FAILED GET /stats ") {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("admin auth failures logged %d times, want 2 (logs: %v)", found, logs)
	}
}
