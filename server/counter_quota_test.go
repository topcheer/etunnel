package server

// Issue #36 regression tests: the per-IP anonymous quota must (a) carry a
// rolling TTL so keys cannot accumulate forever, (b) survive no leaks on
// handshake paths that are rejected after the counter was incremented, and
// (c) recover on its own when a client vanishes without a Bye (SIGKILL
// behind a NAT) - the server's ping liveness closes the session, which
// releases the slot.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/topcheer/etunnel/protocol"
)

// quotaTestServer starts a real Server with an injected in-memory counter
// (New alone does not install one; applyCluster does, which tests never run).
func quotaTestServer(t *testing.T, mut func(*Config)) (*Server, *memCounter, *httptest.Server) {
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
	mc := newMemCounter()
	s.counter = mc
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, mc, ts
}

// dialQuota performs one raw Hello handshake and returns the first control
// message back (Welcome on success, ErrorMsg on rejection).
func dialQuota(t *testing.T, ts *httptest.Server, sub string) (map[string]any, *websocket.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/tunnel", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	hello, _ := json.Marshal(&protocol.Hello{
		Type: protocol.MsgHello, Proto: protocol.ProtoVersion, Subdomain: sub,
	})
	if err := ws.Write(ctx, websocket.MessageText, hello); err != nil {
		t.Fatalf("hello: %v", err)
	}
	_, raw, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("reply not json: %v", err)
	}
	return m, ws
}

func TestMemCounterDecrDeletesAtZero(t *testing.T) {
	ctx := context.Background()
	c := newMemCounter()
	if v, _ := c.Incr(ctx, "k", 2); v != 2 {
		t.Fatalf("incr2: %d", v)
	}
	if err := c.Decr(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if err := c.Decr(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if v, _ := c.Get(ctx, "k"); v != 0 {
		t.Fatalf("zero -> %d", v)
	}
	c.mu.Lock()
	_, hasVal := c.vals["k"]
	_, hasExp := c.exp["k"]
	c.mu.Unlock()
	if hasVal || hasExp {
		t.Fatalf("zeroed key not deleted: vals=%v exp=%v", hasVal, hasExp)
	}
	// A double release must not leave a negative residue either.
	if err := c.Decr(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if v, _ := c.Get(ctx, "k"); v != 0 {
		t.Fatalf("below zero -> %d", v)
	}
}

func TestMemCounterExpiredKeyRestartsAtZero(t *testing.T) {
	ctx := context.Background()
	c := newMemCounter()
	c.Incr(ctx, "k", 3)
	_ = c.SetTTL(ctx, "k", 20*time.Millisecond)
	time.Sleep(40 * time.Millisecond)
	if v, _ := c.Incr(ctx, "k", 1); v != 1 {
		t.Fatalf("expired key resurrected old value: %d", v)
	}
}

func TestPerIPAdmissionSetsRollingTTL(t *testing.T) {
	_, mc, ts := quotaTestServer(t, func(c *Config) { c.MaxTunnelsPerIP = 2 })

	m, ws := dialQuota(t, ts, "")
	if m["type"] != protocol.MsgWelcome {
		t.Fatalf("first handshake rejected: %v", m)
	}
	defer ws.Close(websocket.StatusNormalClosure, "done")

	mc.mu.Lock()
	exp, hasExp := mc.exp["ip:127.0.0.1"]
	val := mc.vals["ip:127.0.0.1"]
	mc.mu.Unlock()
	if val != 1 {
		t.Fatalf("counter value: %d", val)
	}
	if !hasExp || exp.IsZero() {
		t.Fatal("accepted per-IP key has no TTL (issue #36 regression)")
	}
	if left := time.Until(exp); left < 23*time.Hour || left > 25*time.Hour {
		t.Fatalf("TTL not the rolling 24h: %v", left)
	}

	// A second handshake from the same IP fits the quota, a third is refused.
	m2, ws2 := dialQuota(t, ts, "")
	if m2["type"] != protocol.MsgWelcome {
		t.Fatalf("second handshake rejected: %v", m2)
	}
	defer ws2.Close(websocket.StatusNormalClosure, "done")
	m3, ws3 := dialQuota(t, ts, "")
	if m3["type"] != protocol.MsgError || m3["code"] != protocol.ErrCodeTooManyTunnels {
		t.Fatalf("third handshake should hit the per-IP cap: %v", m3)
	}
	defer ws3.Close(websocket.StatusNormalClosure, "done")
}

func TestAdmissionReleasesIPSlotOnRejection(t *testing.T) {
	_, mc, ts := quotaTestServer(t, func(c *Config) { c.MaxTunnelsPerIP = 1 })

	// Anonymous client requests a custom subdomain: rejected after the counter
	// was already incremented. Before #36 this leaked the only slot.
	m, ws := dialQuota(t, ts, "my-sub")
	if m["type"] != protocol.MsgError || m["code"] != protocol.ErrCodeSubdomainPlan {
		t.Fatalf("anonymous custom subdomain should be rejected: %v", m)
	}
	defer ws.Close(websocket.StatusNormalClosure, "done")

	mc.mu.Lock()
	leaked := len(mc.vals)
	mc.mu.Unlock()
	if leaked != 0 {
		t.Fatalf("rejected handshake leaked %d counter key(s)", leaked)
	}

	// The quota must still be usable afterwards.
	m2, ws2 := dialQuota(t, ts, "")
	if m2["type"] != protocol.MsgWelcome {
		t.Fatalf("post-rejection handshake rejected: %v", m2)
	}
	defer ws2.Close(websocket.StatusNormalClosure, "done")
}

// TestPerIPQuotaRecoversAfterClientVanishes is the SIGKILL acceptance test
// from issue #36: a client that stops answering (no Pong, no Bye - exactly
// what kill -9 behind a NAT looks like) must not hold the IP's quota beyond
// the liveness window.
func TestPerIPQuotaRecoversAfterClientVanishes(t *testing.T) {
	_, mc, ts := quotaTestServer(t, func(c *Config) {
		c.MaxTunnelsPerIP = 1
		c.PingInterval = 150 * time.Millisecond
		c.PingTimeout = 600 * time.Millisecond
	})

	m, ws := dialQuota(t, ts, "")
	if m["type"] != protocol.MsgWelcome {
		t.Fatalf("handshake rejected: %v", m)
	}
	defer ws.Close(websocket.StatusNormalClosure, "done")

	// Quota is held while the client is alive-but-silent.
	m2, ws2 := dialQuota(t, ts, "")
	if m2["type"] != protocol.MsgError || m2["code"] != protocol.ErrCodeTooManyTunnels {
		t.Fatalf("quota not enforced while silent client holds it: %v", m2)
	}
	ws2.Close(websocket.StatusNormalClosure, "done")

	// We deliberately never Read again: no Pong will ever be sent, so the
	// server must reap the session at PingTimeout and release the slot.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if v, _ := mc.Get(context.Background(), "ip:127.0.0.1"); v == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if v, _ := mc.Get(context.Background(), "ip:127.0.0.1"); v != 0 {
		t.Fatalf("quota not recovered after liveness window: %d", v)
	}

	// And the freed quota is actually usable again.
	m3, ws3 := dialQuota(t, ts, "")
	if m3["type"] != protocol.MsgWelcome {
		t.Fatalf("quota did not recover: %v", m3)
	}
	defer ws3.Close(websocket.StatusNormalClosure, "done")
}
