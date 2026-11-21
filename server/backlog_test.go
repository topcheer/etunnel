package server

// Backlog batch regressions: #264 serveUpgrade close asymmetry (visitor
// early exit must reclaim the handler, goroutines and stream slot), #268
// forwardUpgraded post-hijack failures answer the raw visitor connection
// instead of falling back to a hijacked ResponseWriter, #265-1
// DowngradeToFree surfaces persistence failures.

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/topcheer/etunnel/protocol"
)

// #264: visitor early exit + silent app must reclaim the handler goroutine
// and the stream slot. Before the fix serveUpgrade parked forever on the
// second <-done: conn.Close wakes the external->tunnel read, but nothing
// wakes the tunnel->external goroutine parked in bodyPr.Read.
func TestServeUpgradeVisitorEarlyExitReclaims(t *testing.T) {
	s := &Server{metrics: &metrics{}, cfg: Config{StreamIdleTimeout: 5 * time.Second}}
	sess := newHOLSession(t)
	st := newHOLStream(t, sess, 9)

	visitor, peer := net.Pipe()
	defer visitor.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		res := &protocol.Res{Type: protocol.MsgRes, StreamID: 9, Status: http.StatusSwitchingProtocols}
		s.serveUpgrade(&hijackRecorder{ResponseRecorder: httptest.NewRecorder(), conn: visitor},
			httptest.NewRequest(http.MethodGet, "https://dev.t.test/ws", nil), sess, st, res)
	}()

	// Consume the replayed 101 head so the splice starts.
	head := make([]byte, 0, 128)
	buf := make([]byte, 1)
	peer.SetReadDeadline(time.Now().Add(3 * time.Second))
	for !bytes.HasSuffix(head, []byte("\r\n\r\n")) {
		if _, err := peer.Read(buf); err != nil {
			t.Fatalf("reading 101 head: %v", err)
		}
		head = append(head, buf[0])
	}

	// The visitor hangs up; the app never sends a byte (push-only WS).
	peer.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("serveUpgrade leaked: visitor early exit did not reclaim the handler (#264)")
	}
	if !st.finished() {
		t.Fatal("stream not finished after visitor early exit (#264)")
	}
	if _, live := sess.streams[9]; live {
		t.Fatal("stream slot 9 still registered - leak (#264)")
	}
}

// #268: post-hijack failure (owner unreachable) must report handled and
// answer 502 on the raw visitor connection, never fall back to writing a
// hijacked ResponseWriter.
func TestForwardUpgradedPostHijackFailsHandled(t *testing.T) {
	visitor, peer := net.Pipe()
	defer visitor.Close()

	req := httptest.NewRequest(http.MethodGet, "https://v.t.test/ws", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")

	// 127.0.0.1:1 refuses instantly - the owner "node" is down. The 502 is
	// written into the synchronous pipe before the call returns, so the
	// reader runs concurrently.
	rawCh := make(chan []byte, 1)
	go func() {
		peer.SetReadDeadline(time.Now().Add(3 * time.Second))
		raw, _ := io.ReadAll(peer)
		rawCh <- raw
	}()
	handled := forwardUpgraded("127.0.0.1:1",
		&hijackRecorder{ResponseRecorder: httptest.NewRecorder(), conn: visitor},
		req, "X-Etunnel-Fwd", "self")
	if !handled {
		t.Fatal("post-hijack failure must report handled (#268)")
	}
	raw := <-rawCh
	if !strings.Contains(string(raw), "502 Bad Gateway") {
		t.Fatalf("visitor got %q, want a 502 on the raw connection (#268)", raw)
	}
}

// #265-1: a downgrade whose persistence fails must surface the error via
// the store's onSaveErr channel (same convention as every sibling mutator).
func TestDowngradeToFreeSurfacesSaveError(t *testing.T) {
	acct := &Account{APIKey: "k", Email: "d@x.com", Tier: "pro"}
	// A directory as the store path: every save fails.
	st := &AccountStore{
		path:   t.TempDir(), // a directory - file writes fail
		byKey:  map[string]*Account{acct.APIKey: acct},
		byMail: map[string]*Account{acct.Email: acct},
	}
	var captured error
	st.onSaveErr = func(err error) { captured = err }

	if !st.DowngradeToFree("d@x.com") {
		t.Fatal("downgrade reported not-found")
	}
	if captured == nil {
		t.Fatal("persistence failure silently swallowed (#265)")
	}
	if st.byMail["d@x.com"].Tier != "free" {
		t.Fatal("in-memory tier should still be downgraded")
	}
}
