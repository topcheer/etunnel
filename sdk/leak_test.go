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
	"sync/atomic"
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

// stubOnceServer answers the first connection with a welcome (then kills it)
// and every later connection with an unauthorized error - a permanent error
// for the supervisor.
func stubOnceServer(t *testing.T) *httptest.Server {
	t.Helper()
	var connects int32
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
		if _, ok := protocol.DecodeControl(raw); ok != nil {
			return
		}
		if atomic.AddInt32(&connects, 1) == 1 {
			wel, _ := protocol.EncodeControl(&protocol.Welcome{
				Type: protocol.MsgWelcome, URL: "https://x.t.test", Subdomain: "x",
			})
			_ = ws.Write(ctx, websocket.MessageText, wel)
			// Kill the first connection so the supervisor dials again.
			time.Sleep(50 * time.Millisecond)
			_ = ws.CloseNow()
			return
		}
		errMsg, _ := protocol.EncodeControl(&protocol.ErrorMsg{
			Type: protocol.MsgError, Code: protocol.ErrCodeUnauthorized, Message: "no",
		})
		_ = ws.Write(ctx, websocket.MessageText, errMsg)
		// Graceful close handshake: the client's handshake-error path calls
		// ws.Close, which waits for the peer's close frame - an abrupt
		// handler return would stall it for the full 5s close timeout.
		_ = ws.Close(websocket.StatusNormalClosure, "denied")
	}))
	t.Cleanup(ts.Close)
	return ts
}

// TestSuperviseExitCancelsContext: a permanent error on reconnect must stop
// the supervisor AND release its context (issue #55: supervise's exit paths
// previously never called r.cancel()).
func TestSuperviseExitCancelsContext(t *testing.T) {
	ts := stubOnceServer(t)
	parent, pcancel := context.WithCancel(context.Background())
	defer pcancel()
	r, err := ConnectReconnecting(parent, Options{
		Server:    "ws" + strings.TrimPrefix(ts.URL, "http") + "/tunnel",
		LocalAddr: "127.0.0.1:1",
		OnLog:     func(string, ...any) {}, // silence reconnect spam
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close("test done")

	// The stub kills the first tunnel; the second dial gets
	// "unauthorized" (permanent) -> supervise must stop.
	select {
	case <-r.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not stop after permanent error")
	}
	select {
	case <-r.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor exit leaked: r.ctx never cancelled (issue #55)")
	}
}
