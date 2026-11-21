package etunnel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/topcheer/etunnel/protocol"
)

// startWelcomeThenPolicyClose accepts one WS connection, answers hello with
// a welcome, then closes the WebSocket with an application-level policy
// close code - the exact shape #266 says collapses to "read closed".
func startWelcomeThenPolicyClose(t *testing.T, code websocket.StatusCode, reason string) *httptest.Server {
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
		if _, derr := protocol.DecodeControl(raw); derr == nil {
			wel, _ := protocol.EncodeControl(&protocol.Welcome{
				Type: protocol.MsgWelcome, URL: "https://x.t.test", Subdomain: "x",
			})
			_ = ws.Write(ctx, websocket.MessageText, wel)
			time.Sleep(50 * time.Millisecond) // let the client receive it
			_ = ws.Close(code, reason)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

// TestCloseReasonSurfacesPeerCloseCode: a server policy close (4403) must
// surface its code/reason through CloseReason() and Err(), not collapse to
// the generic "read closed" (#266).
func TestCloseReasonSurfacesPeerCloseCode(t *testing.T) {
	ts := startWelcomeThenPolicyClose(t, 4403, "quota exceeded")
	tun, err := Connect(context.Background(), Options{
		Server:    "ws" + strings.TrimPrefix(ts.URL, "http") + "/tunnel",
		LocalAddr: "127.0.0.1:1", // never dialed in this test
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close("test done")

	deadline := time.Now().Add(3 * time.Second)
	for tun.CloseReason() == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-tun.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("tunnel did not end after policy close")
	}

	reason := tun.CloseReason()
	if !strings.Contains(reason, "4403") || !strings.Contains(reason, "quota exceeded") {
		t.Fatalf("CloseReason must surface the peer close code/reason (#266), got %q", reason)
	}
	if strings.Contains(reason, "read closed") {
		t.Fatalf("CloseReason must not collapse a policy close to \"read closed\" (#266), got %q", reason)
	}
	te := tun.Err()
	if te == nil {
		t.Fatal("Err() must return the terminal error (#266)")
	}
	var ce *websocket.CloseError
	if !errors.As(te, &ce) || ce.Code != 4403 || ce.Reason != "quota exceeded" {
		t.Fatalf("Err() must surface websocket.CloseError{4403 quota exceeded} (#266), got %v", te)
	}
}
