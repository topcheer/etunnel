package server

// End-to-end mesh test: two meshRouters in one process gossip ownership of a
// subdomain, and the non-owner forwards an HTTP exchange to the owner.

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func newTestMesh(t *testing.T, name string) (*meshRouter, int) {
	t.Helper()
	port := freePort(t)
	// memberlist binds both its gossip port and a TCP probe port; use the
	// same number for simplicity of advertise and a second free port for
	// the internal HTTP listener the Forward target needs.
	r, err := newMeshRouter(fmt.Sprintf("127.0.0.1:%d", port), "", name)
	if err != nil {
		t.Fatalf("mesh %s: %v", name, err)
	}
	t.Cleanup(func() { r.Close() })
	return r, port
}

func TestMeshOwnershipGossipAndForward(t *testing.T) {
	a, aPort := newTestMesh(t, "a")
	b, _ := newTestMesh(t, "b")

	// join b to a
	if n, err := b.ml.Join([]string{fmt.Sprintf("127.0.0.1:%d", aPort+100)}); err != nil || n == 0 {
		t.Fatalf("join: n=%d err=%v", n, err)
	}
	time.Sleep(300 * time.Millisecond) // SWIM membership settle

	// a claims a subdomain
	a.Claim("quiet-frog-42")
	time.Sleep(300 * time.Millisecond) // gossip settle

	// b must resolve the owner as a's advertise address
	if got := b.Lookup("quiet-frog-42"); got == "" || got == b.self {
		t.Fatalf("b.Lookup -> %q (want a=%q)", got, a.self)
	}
	// a resolves itself as owner (Lookup returns "")
	if got := a.Lookup("quiet-frog-42"); got != "" {
		t.Fatalf("a.Lookup -> %q, want \"\"", got)
	}

	// stand up an origin server that a forwards TO (plays the owner's
	// public listener for this test)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello-from-a via %s", r.Header.Get("X-Etunnel-Forward"))
	}))
	defer origin.Close()
	// For the forwarding test we cheat: point b's owner map at the test
	// server so Forward hits it (a real deployment uses a's HTTP port).
	b.mu.Lock()
	b.owner["quiet-frog-42"] = strings.TrimPrefix(origin.URL, "http://")
	b.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://x/dark", nil)
	if !b.Forward("quiet-frog-42", rec, req) {
		t.Fatal("Forward returned false")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "hello-from-a") {
		t.Fatalf("forward body: %q", body)
	}

	// release: ownership disappears everywhere
	a.Release("quiet-frog-42")
	time.Sleep(300 * time.Millisecond)
	if got := b.Lookup("quiet-frog-42"); got != "" && got != b.self {
		t.Fatalf("after release b.Lookup -> %q", got)
	}

	// loop guard: a forwarded request must not forward again
	req2 := httptest.NewRequest(http.MethodGet, "http://x/", nil)
	req2.Header.Set("X-Etunnel-Forward", "someone")
	rec2 := httptest.NewRecorder()
	if b.Forward("quiet-frog-42", rec2, req2) {
		t.Fatal("loop guard failed")
	}
}

// #120: Close must release the claim-broadcast goroutine (stopCh closed,
// Once-guarded) and a second Close must not panic.
func TestMeshCloseStopsClaimBroadcast(t *testing.T) {
	r, err := newMeshRouter("127.0.0.1:0", "", "close-test")
	if err != nil {
		t.Fatalf("mesh: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case <-r.stopCh:
	default:
		t.Fatal("stopCh not closed after Close - claim goroutine would leak")
	}
	if err := r.Close(); err != nil { // second Close: sync.Once must absorb it
		t.Fatalf("second close panicked or failed: %v", err)
	}
}
