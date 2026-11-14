package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBanWSS(t *testing.T) {
	t.Setenv("ETUNNEL_BAN_RANGES", "")
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
		if got := banWSS(c.addr); got != c.want {
			t.Errorf("banWSS(%s) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestBanWSSOverrideAndDisable(t *testing.T) {
	t.Setenv("ETUNNEL_BAN_RANGES", "10.0.0.0/8")
	if !banWSS("10.1.2.3:9") {
		t.Fatal("custom range not honoured")
	}
	if banWSS("104.28.228.8:443") {
		t.Fatal("override must replace the CF snapshot, not add to it")
	}
	t.Setenv("ETUNNEL_BAN_RANGES", "-")
	if banWSS("104.28.228.8:443") {
		t.Fatal("'-' must disable all bans")
	}
}

// The handler refuses banned sources before the WebSocket upgrade.
func TestHandleTunnelWSBansBeforeUpgrade(t *testing.T) {
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
