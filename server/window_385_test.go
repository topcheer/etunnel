package server

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/topcheer/etunnel/protocol"
)

// #385: handshake-vs-delete true window. Handshakes race an account delete
// fired through the REAL admin route handler (the user-visible cascade path
// from #350). Invariant after the delete settles: every tunnel the deleted
// account still holds is freed (branded 404 on the visitor path) - pre-delete
// sessions are cascade-kicked by kickSessionsByAccount, mid-window acquires
// are revoked by the post-acquire re-check (#366), and post-delete handshakes
// are rejected at token resolution. Each handshake is bounded so a hung
// connect cannot stall the test.
func TestHandshakeVsDeleteNoSurvivor(t *testing.T) {
	s, _, ts := quotaTestServer(t, func(c *Config) { c.AdminToken = "tok" })
	// quotaTestServer's wrapper rewrites every Host to the base domain (its
	// admission tests need that); visitor probes here need real vhost
	// routing, so they go through a normalizer that only rewrites IP hosts.
	tsVhost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, _, err := net.SplitHostPort(r.Host); err == nil && net.ParseIP(h) != nil {
			r.Host = "t.test"
		}
		s.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(tsVhost.Close)
	acct, err := s.accounts.Create("win385@x.com", "free")
	if err != nil {
		t.Fatal(err)
	}

	type admitted struct {
		sub string
		ws  *websocket.Conn
	}
	var mu sync.Mutex
	live := make([]admitted, 0, 6)

	// windowDial performs one bounded raw handshake. On a welcome the conn is
	// deliberately left OPEN - a self-closed tunnel would free its own slot
	// and make the no-survivor assertion vacuous.
	windowDial := func() (sub string, ws *websocket.Conn) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/tunnel", nil)
		if err != nil {
			return "", nil
		}
		hello, _ := json.Marshal(&protocol.Hello{
			Type: protocol.MsgHello, Proto: protocol.ProtoVersion, Token: acct.APIKey,
		})
		if err := ws.Write(ctx, websocket.MessageText, hello); err != nil {
			_ = ws.Close(websocket.StatusNormalClosure, "")
			return "", nil
		}
		for {
			_, raw, err := ws.Read(ctx)
			if err != nil {
				// error close (rejected mid-handshake) or timeout
				_ = ws.Close(websocket.StatusNormalClosure, "")
				return "", nil
			}
			var m map[string]any
			if json.Unmarshal(raw, &m) != nil {
				continue
			}
			switch mt, _ := m["type"].(string); mt {
			case "welcome":
				sub, _ = m["subdomain"].(string)
				return sub, ws // conn stays open
			case "error":
				_ = ws.Close(websocket.StatusNormalClosure, "")
				return "", nil
			}
		}
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(delay time.Duration) {
			defer wg.Done()
			<-start
			time.Sleep(delay)
			sub, ws := windowDial()
			if sub == "" {
				return
			}
			mu.Lock()
			live = append(live, admitted{sub: sub, ws: ws})
			mu.Unlock()
			time.Sleep(300 * time.Millisecond) // widen the race window
		}(time.Duration(i) * 20 * time.Millisecond)
	}

	// The delete lands ~40ms in, through the real admin delete handler (the
	// route body: cascade kick + authoritative store delete).
	var delCode int
	go func() {
		<-start
		time.Sleep(40 * time.Millisecond)
		r := httptest.NewRequest(http.MethodDelete, "/admin/accounts/"+acct.APIKey, nil)
		r.Header.Set("X-Admin-Token", "tok")
		w := httptest.NewRecorder()
		s.handleAdminAccountDelete(w, r)
		delCode = w.Code
	}()
	close(start) // release workers + delete goroutine together
	wg.Wait()

	mu.Lock()
	subs := make([]string, 0, len(live))
	for _, a := range live {
		subs = append(subs, a.sub)
	}
	mu.Unlock()
	if delCode != http.StatusNoContent {
		t.Fatalf("admin delete: code=%d, want 204", delCode)
	}
	if len(subs) == 0 {
		t.Fatal("no handshake ever connected - delete raced everything, window not exercised")
	}

	// Settle: cascade kicks, post-acquire re-checks and the async kick
	// finisher run. Then probe every admitted sub: a survivor still answers
	// (502/504 from the stalled raw client), a freed tunnel yields the
	// branded 404. Probes are time-boxed so a live regression fails fast
	// instead of eating the server's 30s stall budget per tunnel.
	cli := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		allFreed := true
		for _, sub := range subs {
			req, _ := http.NewRequest(http.MethodGet, tsVhost.URL+"/", nil)
			req.Host = sub + ".t.test"
			resp, err := cli.Do(req)
			if err != nil {
				allFreed = false
				continue
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				allFreed = false
			}
		}
		if allFreed {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("delete left a live tunnel of a deleted account (#385); subs=%v", subs)
}
