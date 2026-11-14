package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func resetBanCache() { banCacheMu.Lock(); banCache, banCacheBuilt = nil, false; banCacheMu.Unlock() }

func TestBanWSS(t *testing.T) {
	t.Setenv("ETUNNEL_BAN_RANGES", "")
	resetBanCache()
	s, err := New(Config{Domain: "t.test"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		addr string
		want bool
	}{
		{"104.28.228.8:443", true},     // observed Worker client
		{"[2606:4700:1::2]:443", true}, // CF v6
		{"94.192.72.2:5555", false},    // direct client
		{"127.0.0.1:1", false},         // local tests never banned
	}
	for _, c := range cases {
		if got := banWSS(s, c.addr); got != c.want {
			t.Errorf("banWSS(%s) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestBanWSSOverrideAndDisable(t *testing.T) {
	resetBanCache()
	s, err := New(Config{Domain: "t.test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ETUNNEL_BAN_RANGES", "10.0.0.0/8")
	if !banWSS(s, "10.1.2.3:9") {
		t.Fatal("custom range not honoured")
	}
	if banWSS(s, "104.28.228.8:443") {
		t.Fatal("override must replace the CF snapshot, not add to it")
	}
	t.Setenv("ETUNNEL_BAN_RANGES", "-")
	resetBanCache()
	if banWSS(s, "104.28.228.8:443") {
		t.Fatal("'-' must disable all bans")
	}
}

// The handler refuses banned sources before the WebSocket upgrade.
func TestHandleTunnelWSBansBeforeUpgrade(t *testing.T) {
	resetBanCache() // the "-" decision cached by TestBanWSSOverrideAndDisable must not leak in
	s, err := New(Config{Domain: "t.test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ETUNNEL_BAN_RANGES", "")
	req := httptest.NewRequest("GET", "/tunnel", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.RemoteAddr = "104.28.196.5:443"
	rec := httptest.NewRecorder()
	s.handleTunnelWS(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("banned source = %d, want 403 (no upgrade attempt)", rec.Code)
	}
}

// #143: a fully-misconfigured ETUNNEL_BAN_RANGES falls back to the CF
// snapshot (never an empty ban list) and logs once.
func TestBanWSSMisconfigFallsBack(t *testing.T) {
	resetBanCache()
	s, err := New(Config{Domain: "t.test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ETUNNEL_BAN_RANGES", "not;a;cidr")
	if !banWSS(s, "104.28.228.8:443") {
		t.Fatal("zero-parse config must fall back to the CF snapshot, not disable bans")
	}
	if banWSS(s, "94.192.72.2:1") {
		t.Fatal("fallback snapshot must not ban direct clients")
	}
	// cache: changing the env after the first build has no effect (one
	// decision per process, matching the log-once semantics).
	t.Setenv("ETUNNEL_BAN_RANGES", "10.0.0.0/8")
	if banWSS(s, "104.28.228.8:443") == false {
		t.Fatal("cache must keep the first decision for the process lifetime")
	}
}
