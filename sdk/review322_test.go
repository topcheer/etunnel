package etunnel

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

// startFakeTunnel322 accepts one WS connection, completes the hello/welcome
// handshake, then hands the socket to `after` for scenario injection.
func startFakeTunnel322(t *testing.T, after func(ws *websocket.Conn, ctx context.Context)) *httptest.Server {
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
				Type: protocol.MsgWelcome, URL: "https://x.t.test", Subdomain: "x", Proto: 1,
			})
			_ = ws.Write(ctx, websocket.MessageText, wel)
			after(ws, ctx)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func dialFake322(t *testing.T, ts *httptest.Server) *Tunnel {
	t.Helper()
	tun, err := Connect(context.Background(), Options{
		Server:    "ws" + strings.TrimPrefix(ts.URL, "http") + "/tunnel",
		LocalAddr: "127.0.0.1:1", // never dialed in these tests
	})
	if err != nil {
		t.Fatal(err)
	}
	return tun
}

func waitEnded322(t *testing.T, tun *Tunnel) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for tun.CloseReason() == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-tun.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("tunnel did not end")
	}
	return tun.CloseReason()
}

// #322-2 (M2): a malformed KNOWN control frame (truncated envelope) is fatal
// - the tunnel ends with an explicit reason instead of silently continuing.
func TestMalformedKnownControlFrameIsFatal(t *testing.T) {
	ts := startFakeTunnel322(t, func(ws *websocket.Conn, ctx context.Context) {
		_ = ws.Write(ctx, websocket.MessageText, []byte(`{"type":"req"`)) // truncated envelope
		time.Sleep(200 * time.Millisecond)
	})
	tun := dialFake322(t, ts)
	defer tun.Close("test done")
	reason := waitEnded322(t, tun)
	if !strings.Contains(reason, "malformed control frame") {
		t.Fatalf("malformed known frame must be fatal (#322-2), got %q", reason)
	}
}

// #322-2 (M2): an UNKNOWN control type is ignored - the tunnel stays open
// (forward compatibility; ErrUnknownControlType must not become fatal).
func TestUnknownControlTypeIgnored(t *testing.T) {
	ts := startFakeTunnel322(t, func(ws *websocket.Conn, ctx context.Context) {
		_ = ws.Write(ctx, websocket.MessageText, []byte(`{"type":"zzz-future-extension","x":1}`))
		time.Sleep(400 * time.Millisecond)
	})
	tun := dialFake322(t, ts)
	defer tun.Close("test done")
	deadline := time.Now().Add(600 * time.Millisecond)
	for tun.CloseReason() == "" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if reason := tun.CloseReason(); reason != "" {
		t.Fatalf("unknown control type must be ignored (#322-2), got %q", reason)
	}
}

// #322-5: a mid-session server ErrorMsg ends the tunnel with the real
// reason instead of vanishing.
func TestMidSessionErrorMsgEndsWithReason(t *testing.T) {
	ts := startFakeTunnel322(t, func(ws *websocket.Conn, ctx context.Context) {
		em, _ := protocol.EncodeControl(&protocol.ErrorMsg{
			Type: "error", Code: "quota", Message: "too many tunnels",
		})
		_ = ws.Write(ctx, websocket.MessageText, em)
	})
	tun := dialFake322(t, ts)
	defer tun.Close("test done")
	reason := waitEnded322(t, tun)
	if !strings.Contains(reason, "too many tunnels") {
		t.Fatalf("ErrorMsg must end the tunnel with its message (#322-5), got %q", reason)
	}
}

// #322-6: a welcome advertising proto=2 must be rejected loudly.
func TestWelcomeProto2Rejected(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx := r.Context()
		if _, raw, err := ws.Read(ctx); err == nil {
			if _, derr := protocol.DecodeControl(raw); derr == nil {
				wel, _ := protocol.EncodeControl(&protocol.Welcome{
					Type: protocol.MsgWelcome, URL: "https://x.t.test", Subdomain: "x", Proto: 2,
				})
				_ = ws.Write(ctx, websocket.MessageText, wel)
				time.Sleep(200 * time.Millisecond)
			}
		}
	}))
	t.Cleanup(ts.Close)

	_, err := Connect(context.Background(), Options{
		Server:    "ws" + strings.TrimPrefix(ts.URL, "http") + "/tunnel",
		LocalAddr: "127.0.0.1:1",
	})
	if err == nil || !strings.Contains(err.Error(), "proto") {
		t.Fatalf("welcome.proto=2 must be rejected with a proto error (#322-6), got %v", err)
	}
}
