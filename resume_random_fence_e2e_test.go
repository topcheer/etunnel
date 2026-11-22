package etunnel_test

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/topcheer/etunnel/protocol"
)

// #331 M1: sequential replay of four random-subdomain resume tokens under
// MaxTunnels 2 - the first two replays are admitted (each keeps its original
// random subdomain); replays three and four hit the in-lock account limit
// and are answered with an error close, never a welcome. The concurrent
// variant of this fence is pinned at the registry level by
// TestAcquireBoundedTunnelsAtomic (the handshake path calls the same
// AcquireBounded).
func TestResumeRandomTokensSequentialMaxTunnelsFence(t *testing.T) {
	srv, srvURL := startTestServer(t, nil)

	acct, err := srv.Accounts().Create("fence@example.com", "free") // MaxTunnels 2
	if err != nil {
		t.Fatal(err)
	}

	// Harvest four random-subdomain resume tokens sequentially (each
	// connect closes before the next, so harvesting never hits the limit).
	tokens := make([]string, 0, 4)
	origins := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		w, close1 := rawResumeHello(t, srvURL, protocol.Hello{Token: acct.APIKey})
		if w.ResumeToken == "" || w.Subdomain == "" {
			t.Fatalf("harvest #%d: sub=%q tok=%q", i, w.Subdomain, w.ResumeToken)
		}
		tokens = append(tokens, w.ResumeToken)
		origins = append(origins, w.Subdomain)
		close1()
	}

	// Replays one and two: admitted (original subdomain kept) and held LIVE
	// - the fence only shows while the quota is actually occupied.
	closers := make([]func(), 0, 2)
	for i := 0; i < 2; i++ {
		w, closeFn := rawResumeHello(t, srvURL, protocol.Hello{
			Token: acct.APIKey, Resume: tokens[i],
		})
		if w.Subdomain != origins[i] {
			t.Fatalf("replay #%d: fence should admit %q, got %q (#331 M1)", i+1, origins[i], w.Subdomain)
		}
		closers = append(closers, closeFn)
	}

	// Replays three and four: both quota slots are live -> the in-lock
	// fence rejects them; the rejected handshakes get an error close (no
	// welcome, no fresh random name).
	for i := 2; i < 4; i++ {
		w, closeFn := rawResumeHelloAllowError(t, srvURL, protocol.Hello{
			Token: acct.APIKey, Resume: tokens[i],
		})
		closeFn()
		if w.Subdomain != "" {
			t.Fatalf("over-quota replay #%d admitted %q (#331 M1)", i+1, w.Subdomain)
		}
	}
	for _, c := range closers {
		c()
	}
}

func rawResumeHelloAllowError(t *testing.T, srvURL string, hello protocol.Hello) (protocol.Welcome, func()) {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srvURL, "http") + "/tunnel"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		cancel()
		return protocol.Welcome{}, func() { cancel() }
	}
	hello.Type = "hello"
	hello.Proto = protocol.ProtoVersion
	raw, _ := json.Marshal(hello)
	if err := ws.Write(ctx, websocket.MessageText, raw); err != nil {
		ws.Close(websocket.StatusNormalClosure, "")
		cancel()
		return protocol.Welcome{}, func() { cancel() }
	}
	var w protocol.Welcome
	for {
		_, rd, err := ws.Reader(ctx)
		if err != nil {
			ws.Close(websocket.StatusNormalClosure, "")
			cancel()
			return protocol.Welcome{}, func() { cancel() }
		}
		msg, err := io.ReadAll(rd)
		if err != nil {
			ws.Close(websocket.StatusNormalClosure, "")
			cancel()
			return protocol.Welcome{}, func() { cancel() }
		}
		var m map[string]any
		if json.Unmarshal(msg, &m) != nil {
			continue
		}
		if mt, _ := m["type"].(string); mt == "welcome" {
			if err := json.Unmarshal(msg, &w); err != nil {
				w = protocol.Welcome{}
			}
			break
		}
		if mt, _ := m["type"].(string); mt == "error" {
			break // rejected: no welcome coming
		}
	}
	return w, func() {
		ws.Close(websocket.StatusNormalClosure, "")
		cancel()
	}
}
