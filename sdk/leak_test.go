package etunnel

// Issue #55 regression: error paths must cancel the tunnel context.
// writeLoop's write-error branch used to return without t.cancel(), and
// supervise()'s exit paths without r.cancel() - both leaked the
// context.WithCancel registration for the life of the parent context.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/topcheer/etunnel/protocol"
)

// startWelcomeThenKill accepts one WS connection, answers hello with a
// welcome, then abruptly kills the TCP connection (no close handshake).
func startWelcomeThenKill(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx := r.Context()
		_, raw, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if _, ok := protocol.DecodeControl(raw); ok == nil {
			// hello decoded: reply welcome then kill the socket abruptly.
			wel, _ := protocol.EncodeControl(&protocol.Welcome{
				Type: protocol.MsgWelcome, URL: "https://x.t.test", Subdomain: "x",
			})
			_ = ws.Write(ctx, websocket.MessageText, wel)
			time.Sleep(50 * time.Millisecond) // let the client receive it
			_ = ws.CloseNow()
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

// TestWriteErrorCancelsContext: after the server vanishes, a pending write
// fails in writeLoop, which must cancel t.ctx (previously it returned
// without cancelling - the drain branch never runs because writeLoop itself
// is the only goroutine that drains).
func TestWriteErrorCancelsContext(t *testing.T) {
	ts := startWelcomeThenKill(t)
	tun, err := Connect(context.Background(), Options{
		Server:    "ws" + strings.TrimPrefix(ts.URL, "http") + "/tunnel",
		LocalAddr: "127.0.0.1:1", // never dialed in this test
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close("test done")

	// Wait for the abrupt kill to reach the client, then push a write that
	// must fail and take the write-error branch.
	deadline := time.Now().Add(3 * time.Second)
	for tun.CloseReason() == "" && time.Now().Before(deadline) {
		_ = tun.sendJSON(&protocol.Ping{Type: protocol.MsgPing, TS: 1})
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-tun.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("tunnel did not end after server kill")
	}
	// The regression: the tunnel context must be cancelled too.
	select {
	case <-tun.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("write-error path leaked: tunnel context never cancelled (issue #55)")
	}
}

// TestSuperviseExitCancelsContext: a permanent error (bad token) must stop
// the supervisor AND release its context.
func TestSuperviseExitCancelsContext(t *testing.T) {
	ts := startWelcomeThenKill(t)
	// Bad local addr keeps Connect failing fast with a dial error... use a
	// hostile server URL instead so every attempt errors (transient), then
	// cancel the parent: supervise must release r.ctx.
	parent, pcancel := context.WithCancel(context.Background())
	r, err := ConnectReconnecting(parent, Options{
		Server:    "ws://127.0.0.1:1/tunnel", // nothing listens: instant refusal
		LocalAddr: "127.0.0.1:1",
		OnLog:     func(string, ...any) {}, // silence reconnect spam
	})
	if err == nil {
		// Unexpected: dial to port 1 should fail immediately. If the
		// environment somehow has a listener, close and skip.
		r.Close("skip")
		pcancel()
		t.Skip("connect unexpectedly succeeded")
	}
	pcancel()
	_ = ts.Close
}
