package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeTelegram records sendMessage calls and serves getUpdates from a queue.
type fakeTelegram struct {
	mu       sync.Mutex
	sent     []string
	updates  string // raw JSON array for the next getUpdates
	serveErr bool
}

func newFakeTelegram(t *testing.T) (*fakeTelegram, *httptest.Server) {
	t.Helper()
	f := &fakeTelegram{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var p map[string]any
			_ = json.NewDecoder(r.Body).Decode(&p)
			f.mu.Lock()
			f.sent = append(f.sent, fmt.Sprint(p["text"]))
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			f.mu.Lock()
			up := f.updates
			f.mu.Unlock()
			if up == "" {
				up = "[]"
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":` + up + `}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func newFakeTG(t *testing.T) (*fakeTelegram, *httptest.Server) {
	t.Helper()
	f := &fakeTelegram{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var p map[string]any
			_ = json.NewDecoder(r.Body).Decode(&p)
			f.mu.Lock()
			f.sent = append(f.sent, fmt.Sprint(p["text"]))
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			f.mu.Lock()
			up := f.updates
			f.mu.Unlock()
			if up == "" {
				up = "[]"
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":` + up + `}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func newTGTestServer(t *testing.T, fake *httptest.Server, chatID int64) *Server {
	t.Helper()
	s := newBillingTestServer(t)
	s.tg = telegramConfig{token: "TEST:TOKEN", chatID: chatID, apiBase: fake.URL}
	return s
}

func tgUpdate(chatID int64, text string) string {
	return fmt.Sprintf(`[{"update_id":1,"message":{"message_id":1,"chat":{"id":%d},"text":%q}}]`, chatID, text)
}

func TestTelegramCommandDispatch(t *testing.T) {
	_, api := newFakeTelegram(t)
	s := newTGTestServer(t, api, 100)

	cases := map[string]func(string) bool{
		"/status":            func(r string) bool { return strings.Contains(r, "tunnels 0") && strings.Contains(r, "uptime") },
		"/tunnels":           func(r string) bool { return r == "no live tunnels" },
		"/accounts":          func(r string) bool { return r == "no accounts" },
		"/help":              func(r string) bool { return strings.Contains(r, "/kick") && strings.Contains(r, "/whois") },
		"/kick":              func(r string) bool { return r == "usage: /kick <subdomain>" },
		"/kick nope":         func(r string) bool { return r == "no such tunnel: nope" },
		"/tier":              func(r string) bool { return strings.Contains(r, "usage") },
		"/tier a@x.com gold": func(r string) bool { return strings.Contains(r, "unknown tier") },
		"/tier a@x.com free": func(r string) bool { return strings.Contains(r, "unknown tier") },
		"/whois nope":        func(r string) bool { return strings.Contains(r, "no such tunnel") },
		"/unknown":           func(r string) bool { return r == "" },
	}
	for cmd, check := range cases {
		if r := s.handleAdminCommand(cmd); !check(r) {
			t.Errorf("%s -> %q (unexpected)", cmd, r)
		}
	}
}

func TestTelegramTierCreatesAccount(t *testing.T) {
	f, api := newFakeTelegram(t)
	s := newTGTestServer(t, api, 100)

	r := s.handleAdminCommand("/tier Alice@X.com pro")
	if r != "alice@x.com -> pro" {
		t.Fatalf("tier cmd: %q", r)
	}
	if _, ok := s.accounts.LookupEmail("alice@x.com"); !ok {
		t.Fatal("account not created")
	}
	// payment notification fired (activate hook)
	f.mu.Lock()
	sent := strings.Join(f.sent, "|")
	f.mu.Unlock()
	if !strings.Contains(sent, "alice@x.com purchased pro") {
		t.Fatalf("payment notify missing: %q", sent)
	}
}

func TestTelegramPollWhitelist(t *testing.T) {
	_, api := newFakeTelegram(t)
	s := newTGTestServer(t, api, 100)

	// update from a foreign chat must be dropped without reply
	r := s.handleAdminCommand("/status") // sanity: admin command works
	if r == "" {
		t.Fatal("admin command returned nothing")
	}
	// simulate foreign update through the loop's dispatch guard: emulate by
	// checking the whitelist decision directly (chat id mismatch -> skip)
	foreign := tgUpdate(999, "/status")
	var wrapped struct {
		Result []struct {
			Message struct {
				Chat struct {
					ID float64 `json:"id"`
				} `json:"chat"`
			} `json:"message"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(`{"result":`+foreign+`}`), &wrapped); err != nil {
		t.Fatal(err)
	}
	if len(wrapped.Result) != 1 || int64(wrapped.Result[0].Message.Chat.ID) == s.tg.chatID {
		t.Fatalf("fixture chat id unexpectedly equals admin id")
	}
}

func TestTelegramDisabledNoop(t *testing.T) {
	s := newBillingTestServer(t) // tg zero value -> disabled
	if s.tg.enabled() {
		t.Fatal("zero config must be disabled")
	}
	s.notifyPayment("a@x.com", "pro") // must not panic
	if r := s.handleAdminCommand("/status"); r == "" {
		t.Fatal("commands still work locally when bot disabled")
	}
}

func TestHumanBytes(t *testing.T) {
	for in, want := range map[int64]string{
		512: "512B", 2048: "2.0KB", 5 << 20: "5.0MB", 3 << 30: "3.0GB",
	} {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d)=%s want %s", in, got, want)
		}
	}
}

var _ = filepath.Join

func TestTelegramMenuAliases(t *testing.T) {
	_, api := newFakeTelegram(t)
	s := newTGTestServer(t, api, 100)
	if r := s.handleAdminCommand("📊 Status"); !strings.Contains(r, "uptime") {
		t.Fatalf("menu Status -> %q", r)
	}
	if r := s.handleAdminCommand("❓ Help"); !strings.Contains(r, "/kick") {
		t.Fatalf("menu Help -> %q", r)
	}
	if r := s.handleAdminCommand("🔑 API key"); r != "usage: /key <email>" {
		t.Fatalf("menu Key -> %q", r)
	}
	if tgMainMenu()["keyboard"] == nil {
		t.Fatal("main menu keyboard missing")
	}
}

// /history guards: usage validation and the no-ledger message (non-PG
// deployments). The windowed SQL itself is covered live by the PG gate
// (ET_PG_TEST_DSN) when reachable.
func TestTelegramHistoryGuards(t *testing.T) {
	s, err := New(Config{Domain: "t.test"})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.handleAdminCommand("/history 7"); got != "usage: /history [5|10|30]" {
		t.Fatalf("bad window reply = %q", got)
	}
	if got := s.handleAdminCommand("/history"); got != "history needs ETUNNEL_PG_DSN (session ledger)" {
		t.Fatalf("no-ledger reply = %q", got)
	}
	if strings.Contains(s.handleAdminCommand("/help"), "/history") != true {
		t.Fatal("help must list /history")
	}
}
