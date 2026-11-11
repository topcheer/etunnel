package server

// Live test against the Redis on host 13 (audiobook-redis, db 15 to avoid
// touching the audiobook data). Skipped when 13 is unreachable so CI on
// GitHub does not fail.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/topcheer/etunnel/protocol"
)

func TestRedisCounterLive(t *testing.T) {
	redisURL := os.Getenv("ET_REDIS_TEST_URL")
	if redisURL == "" {
		t.Skip("ET_REDIS_TEST_URL not set (live Redis test)")
	}
	c, err := newRedisCounter(redisURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()
	ctx := context.Background()
	// cleanup then incr/decr roundtrip
	c.rdb.Del(ctx, "et:test:ip")
	v, err := c.Incr(ctx, "test:ip", 1)
	if err != nil || v != 1 {
		t.Fatalf("Incr -> %d %v", v, err)
	}
	v, _ = c.Incr(ctx, "test:ip", 1)
	if v != 2 {
		t.Fatalf("second Incr -> %d", v)
	}
	if err := c.Decr(ctx, "test:ip"); err != nil {
		t.Fatal(err)
	}
	if v, _ := c.Get(ctx, "test:ip"); v != 1 {
		t.Fatalf("Get after Decr -> %d", v)
	}
	// TTL
	if err := c.SetTTL(ctx, "test:ip", 1100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1300 * time.Millisecond)
	if v, _ := c.Get(ctx, "test:ip"); v != 0 {
		t.Fatalf("expired -> %d", v)
	}
	// never
	c.Incr(ctx, "test:acct", 3)
	c.SetTTL(ctx, "test:acct", TTLNever)
	time.Sleep(30 * time.Millisecond)
	if v, _ := c.Get(ctx, "test:acct"); v != 3 {
		t.Fatalf("TTLNever -> %d", v)
	}
	c.rdb.Del(ctx, "et:test:acct")

	// issue #36: a decrement that reaches zero must DELETE the key, and a
	// double release must not leave a negative residue behind.
	c.rdb.Del(ctx, "et:test:zero")
	c.Incr(ctx, "test:zero", 1)
	if err := c.Decr(ctx, "test:zero"); err != nil {
		t.Fatal(err)
	}
	if n, err := c.rdb.Exists(ctx, "et:test:zero").Result(); err != nil || n != 0 {
		t.Fatalf("zeroed key still exists: n=%d err=%v", n, err)
	}
	if err := c.Decr(ctx, "test:zero"); err != nil { // release without a slot
		t.Fatal(err)
	}
	if v, _ := c.Get(ctx, "test:zero"); v != 0 {
		t.Fatalf("negative residue: %d", v)
	}
	if n, _ := c.rdb.Exists(ctx, "et:test:zero").Result(); n != 0 {
		t.Fatal("negative-residue key exists")
	}
}

// TestPerIPAdmissionSetsRedisTTL is the issue #36 acceptance on the real
// backend: an admitted anonymous tunnel must leave an et:ip:<addr> key WITH
// an expiry, and a clean disconnect must delete it (no TTL=-1, no zero-valued
// residue).
func TestPerIPAdmissionSetsRedisTTL(t *testing.T) {
	redisURL := os.Getenv("ET_REDIS_TEST_URL")
	if redisURL == "" {
		t.Skip("ET_REDIS_TEST_URL not set (live Redis test)")
	}
	c, err := newRedisCounter(redisURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()
	ctx := context.Background()
	c.rdb.Del(ctx, "et:ip:127.0.0.1")

	srv, err := New(Config{
		Domain: "t.test", WSPath: "/tunnel",
		TLS:             TLSConfig{Mode: TLSModeHTTP},
		AccountsFile:    filepath.Join(t.TempDir(), "a.json"),
		Tiers:           DefaultTiers(),
		MaxTunnelsPerIP: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.counter = c
	// #35/#44: the WS endpoint serves the base domain only; httptest's
	// Host is 127.0.0.1:port, which routing sends straight to the proxy.
	// Rewrite it to the base domain so the handshake reaches the endpoint.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "t.test"
		srv.Handler().ServeHTTP(w, r)
	}))
	defer ts.Close()

	wsCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(wsCtx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/tunnel", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	hello, _ := json.Marshal(&protocol.Hello{Type: protocol.MsgHello, Proto: protocol.ProtoVersion})
	if err := ws.Write(wsCtx, websocket.MessageText, hello); err != nil {
		t.Fatalf("hello: %v", err)
	}
	_, raw, err := ws.Read(wsCtx)
	if err != nil {
		t.Fatalf("welcome: %v", err)
	}
	var w map[string]any
	if json.Unmarshal(raw, &w) != nil || w["type"] != protocol.MsgWelcome {
		t.Fatalf("handshake rejected: %s", raw)
	}

	// The admitted key must carry an expiry (issue #36: it used to be -1).
	ttl, err := c.rdb.TTL(ctx, "et:ip:127.0.0.1").Result()
	if err != nil || ttl <= 0 {
		t.Fatalf("et:ip key TTL: %v err=%v (want >0)", ttl, err)
	}

	// Clean disconnect: the slot is released and the key disappears entirely.
	ws.Close(websocket.StatusNormalClosure, "done")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if n, _ := c.rdb.Exists(ctx, "et:ip:127.0.0.1").Result(); n == 0 {
			return // released and deleted: acceptance met
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("disconnect did not release+delete the per-IP key")
}
