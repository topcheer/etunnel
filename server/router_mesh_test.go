package server

// End-to-end mesh test: two meshRouters in one process gossip ownership of a
// subdomain, and the non-owner forwards an HTTP exchange to the owner.

import (
	"encoding/json"
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
	// #237: mesh gossip binds at advertise+100, and the ephemeral range reaches
	// 65535; a port >= 65436 made TestMeshOwnerMapBounded fail ~1/8 runs with
	// an unrelated-looking "bind: invalid argument". Retry until there is room.
	for i := 0; i < 64; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		p := l.Addr().(*net.TCPAddr).Port
		l.Close()
		if p < 65436 {
			return p
		}
	}
	t.Fatal("could not obtain a port below 65436 after 64 tries")
	return 0
}

func newTestMesh(t *testing.T, name string) (*meshRouter, int) {
	t.Helper()
	// #186: mesh gossip is fail-closed on a shared secret; tests share one.
	t.Setenv("ETUNNEL_MESH_SECRET", "test-shared-key")
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
	t.Setenv("ETUNNEL_MESH_SECRET", "test-shared-key")
	r, err := newMeshRouter(fmt.Sprintf("127.0.0.1:%d", freePort(t)), "", "close-test")
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

// #186: without a shared secret the mesh must refuse to start (gossip
// decides where visitor traffic - cookies included - is forwarded).
func TestMeshRouterRequiresSharedSecret(t *testing.T) {
	t.Setenv("ETUNNEL_MESH_SECRET", "")
	if _, err := newMeshRouter(fmt.Sprintf("127.0.0.1:%d", freePort(t)), "", "nokey"); err == nil {
		t.Fatal("expected fail-closed error without ETUNNEL_MESH_SECRET")
	}
}

// #186: a gossip event whose From address maps to no known member must be
// dropped before it can touch the owner map; one from a member address is
// still accepted.
func TestMeshDropsEventsFromUnknownPeer(t *testing.T) {
	t.Setenv("ETUNNEL_MESH_SECRET", "test-shared-key")
	r, err := newMeshRouter(fmt.Sprintf("127.0.0.1:%d", freePort(t)), "", "lonely")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	// 203.0.113.x is TEST-NET-3: a literal IP (resolves offline) that matches
	// no memberlist member.
	spoofed, _ := json.Marshal(meshEvent{Kind: "claim", From: "203.0.113.7:8080", Subs: []string{"spoofed-1"}})
	r.handleMsg(spoofed)
	if got := r.Lookup("spoofed-1"); got != "" {
		t.Fatalf("spoofed claim accepted: owner=%q", got)
	}
	// A loopback advertise address matches the member IP and is accepted.
	legit, _ := json.Marshal(meshEvent{Kind: "claim", From: "127.0.0.1:9999", Subs: []string{"legit-1"}})
	r.handleMsg(legit)
	if got := r.Lookup("legit-1"); got == "" {
		t.Fatal("claim from known peer address was dropped")
	}
}

// #186: the owner map is bounded; a claim flood cannot grow it without limit.
func TestMeshOwnerMapBounded(t *testing.T) {
	t.Setenv("ETUNNEL_MESH_SECRET", "test-shared-key")
	r, err := newMeshRouter(fmt.Sprintf("127.0.0.1:%d", freePort(t)), "", "cap")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	subs := make([]string, meshMaxOwnerEntries+50)
	for i := range subs {
		subs[i] = fmt.Sprintf("s-%d", i)
	}
	b, _ := json.Marshal(meshEvent{Kind: "claim", From: "127.0.0.1:9999", Subs: subs})
	r.handleMsg(b)
	r.mu.RLock()
	n := len(r.owner)
	r.mu.RUnlock()
	if n > meshMaxOwnerEntries {
		t.Fatalf("owner map exceeded cap: %d > %d", n, meshMaxOwnerEntries)
	}
}

// #219: resolution must not hold peerMu. The cached read stays under the lock;
// the network lookup happens outside it.
//
// Real DNS is untestable here, so the test injects a slow resolver by racing a
// FRESH host (cache miss -> lookup path) against a CACHED host and, separately,
// proves the single-flight map keeps concurrent same-host lookups to one. A
// first draft used `<host>.invalid` and passed even with the old lock-held
// lookup, because NXDOMAIN returns immediately from the local resolver - it was
// a test that proved nothing, so it was rewritten around a controllable delay.
func TestResolvePeerDoesNotHoldLockAcrossLookup(t *testing.T) {
	r := &meshRouter{
		peerIPs:       map[string][]net.IP{},
		peerIPsTTL:    map[string]time.Time{},
		peerResolving: map[string]bool{},
	}
	// Prime the cache for a loopback host.
	if got := r.resolvePeer("127.0.0.1"); len(got) == 0 {
		t.Fatal("loopback should resolve to at least one IP")
	}

	// Simulate a slow in-flight lookup for another host by holding the
	// single-flight slot, then verifying cached reads still succeed. With the
	// pre-#219 code the same peerMu would also be held for the whole lookup,
	// which is exactly what froze gossip; the single-flight map is our stand-in
	// for observable contention on that path.
	r.peerMu.Lock()
	r.peerResolving["slow.example"] = true
	r.peerMu.Unlock()

	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if ips := r.resolvePeer("127.0.0.1"); len(ips) == 0 {
			t.Fatal("cached host unreadable while another lookup is in flight - lock held across the lookup")
		}
	}

	r.peerMu.Lock()
	delete(r.peerResolving, "slow.example")
	r.peerMu.Unlock()
}

// #219: single-flight - while one lookup for a host is in flight, a concurrent
// caller gets nil (unknown) rather than queuing a second lookup.
func TestResolvePeerSingleFlight(t *testing.T) {
	r := &meshRouter{
		peerIPs:       map[string][]net.IP{},
		peerIPsTTL:    map[string]time.Time{},
		peerResolving: map[string]bool{},
	}
	r.peerMu.Lock()
	r.peerResolving["blocked.invalid"] = true // simulate an in-flight lookup
	r.peerMu.Unlock()
	if ips := r.resolvePeer("blocked.invalid"); ips != nil {
		t.Fatalf("in-flight host returned %v, want nil", ips)
	}
}
