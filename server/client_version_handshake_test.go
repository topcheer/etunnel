package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/topcheer/etunnel/protocol"
)

// newVersionGateServer mirrors the admission-test setup (counter_quota_test):
// base-domain Host rewrite so the dial reaches the real handshake path.
func newVersionGateServer(t *testing.T, mut func(*Config)) *httptest.Server {
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
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "t.test"
		s.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// dialHello sends one Hello with the given version and returns the first
// control message back (Welcome on success, ErrorMsg on rejection).
func dialHello(t *testing.T, ts *httptest.Server, version string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/tunnel", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
	hello, _ := json.Marshal(&protocol.Hello{
		Type: protocol.MsgHello, Proto: protocol.ProtoVersion, Version: version,
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
	return m
}

// End-to-end gate on the real handshake path: server 1.7.40 (lag 5) admits
// 1.7.35, rejects 1.7.34 and version-less hellos with client_outdated + URL.
func TestHandshakeClientVersionGate(t *testing.T) {
	const url = "https://dl.example/etunnel"
	setServerVersion(t, "1.7.40")
	ts := newVersionGateServer(t, func(c *Config) { c.ClientDownloadURL = url })

	if m := dialHello(t, ts, "1.7.35"); m["type"] != "welcome" {
		t.Fatalf("1.7.35 (lag 5): want welcome, got %v", m)
	}
	if m := dialHello(t, ts, "1.7.41"); m["type"] != "welcome" {
		t.Fatalf("1.7.41 (newer): want welcome, got %v", m)
	}
	for _, ver := range []string{"", "garbage", "1.7.34", "1.6.99"} {
		m := dialHello(t, ts, ver)
		if m["type"] != "error" || m["code"] != protocol.ErrCodeClientOutdated {
			t.Fatalf("hello version %q: want error %s, got %v", ver, protocol.ErrCodeClientOutdated, m)
		}
		msg, _ := m["message"].(string)
		for _, sub := range []string{"Upgrade: " + url, "this server runs 1.7.40", "requires SDK >= 1.7.35"} {
			if !strings.Contains(msg, sub) {
				t.Errorf("hello version %q: message %q misses %q", ver, msg, sub)
			}
		}
	}
}

// -client-version-lag -1 (or a dev build) keeps the gate open.
func TestHandshakeClientVersionGateDisabled(t *testing.T) {
	setServerVersion(t, "1.7.40")
	ts := newVersionGateServer(t, func(c *Config) { c.ClientVersionLag = -1 })
	if m := dialHello(t, ts, ""); m["type"] != "welcome" {
		t.Fatalf("gate off: version-less hello want welcome, got %v", m)
	}
	setServerVersion(t, "dev")
	ts2 := newVersionGateServer(t, nil)
	if m := dialHello(t, ts2, "garbage"); m["type"] != "welcome" {
		t.Fatalf("dev server: policy must be off, got %v", m)
	}
}
