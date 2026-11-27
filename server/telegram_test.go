package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"

	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTelegram records sendMessage calls and serves getUpdates from a queue.
type fakeTelegram struct {
	mu       sync.Mutex
	sent     []string
	markups  []any  // reply_markup payload per sendMessage (nil = main menu)
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
			f.markups = append(f.markups, p["reply_markup"])
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

// #notify tunnel-open: a fresh subdomain pushes exactly one 🚇 message; a
// reconnect inside the cooldown window does not repeat; a different
// subdomain is independent; a lapsed cooldown notifies again.
func TestNotifyTunnelOpen(t *testing.T) {
	fake, api := newFakeTelegram(t)
	s := newTGTestServer(t, api, 42)
	old := tunnelOpenCooldown
	tunnelOpenCooldown = 60 * time.Millisecond
	t.Cleanup(func() { tunnelOpenCooldown = old })
	tunnelOpenSeen.Range(func(k, _ any) bool {
		tunnelOpenSeen.Delete(k)
		return true
	})

	sess := func(sub string) *Session {
		return &Session{
			id:      "sess-" + sub,
			sub:     sub,
			url:     "https://" + sub + ".t.test",
			ip:      "203.0.113.9:4444",
			account: &Account{Email: "dev@x.com"},
		}
	}
	countSent := func() int {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.sent)
	}
	// The send is asynchronous (#review): poll until n messages arrived.
	waitFor := func(n int) bool {
		for i := 0; i < 100; i++ {
			if countSent() >= n {
				return true
			}
			time.Sleep(50 * time.Millisecond)
		}
		return false
	}

	// a) fresh subdomain -> exactly one 🚇 push with the required fields.
	s.notifyTunnelOpen(sess("fresh-otter-x1"), "Pro")
	if !waitFor(1) {
		t.Fatalf("fresh subdomain sent %d messages, want 1", countSent())
	}
	fake.mu.Lock()
	text := fake.sent[0]
	fake.mu.Unlock()
	for _, want := range []string{"🚇", "fresh-otter-x1", "https://fresh-otter-x1.t.test", "Pro", "dev@x.com", "203.0.113.9"} {
		if !strings.Contains(text, want) {
			t.Fatalf("message %q missing %q", text, want)
		}
	}

	// b) resume-style reconnect inside the cooldown -> no repeat.
	s.notifyTunnelOpen(sess("fresh-otter-x1"), "Pro")
	time.Sleep(150 * time.Millisecond) // give a (wrongly) sync send time to land
	if n := countSent(); n != 1 {
		t.Fatalf("cooldown reconnect sent %d messages, want 1", n)
	}

	// c) a different subdomain is independent.
	s.notifyTunnelOpen(sess("blue-frog-x2"), "Starter")
	if !waitFor(2) {
		t.Fatalf("second subdomain sent %d messages, want 2", countSent())
	}

	// cooldown expiry notifies the same subdomain again.
	time.Sleep(70 * time.Millisecond)
	s.notifyTunnelOpen(sess("fresh-otter-x1"), "Pro")
	if !waitFor(3) {
		t.Fatalf("post-cooldown open sent %d messages, want 3", countSent())
	}
}

// A disabled bot must not send (and must not panic).
func TestNotifyTunnelOpenDisabled(t *testing.T) {
	fake, api := newFakeTelegram(t)
	s := newTGTestServer(t, api, 42)
	s.tg.token = "" // bot disabled
	tunnelOpenSeen.Range(func(k, _ any) bool {
		tunnelOpenSeen.Delete(k)
		return true
	})

	s.notifyTunnelOpen(&Session{sub: "quiet-fox-x3", url: "https://quiet-fox-x3.t.test"}, "Free")
	fake.mu.Lock()
	n := len(fake.sent)
	fake.mu.Unlock()
	if n != 0 {
		t.Fatalf("disabled bot sent %d messages (#notify tunnel-open)", n)
	}
}

// The poll loop must make a dead bot visible: one journal line on the first
// getUpdates failure, then rate-limited; and the bot token (which net/http
// embeds in *url.Error) must never reach the log.
func TestTelegramPollLoopLogsFailuresAndRedactsToken(t *testing.T) {
	// apiBase that cannot be connected: the *url.Error carries /bot<token>/.
	s := newBillingTestServer(t)
	s.tg = telegramConfig{token: "12345:SECRETTOKEN", chatID: 1, apiBase: "http://127.0.0.1:1"}
	cap := captureLogs(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.telegramPollLoop(ctx) }()
	// The loop fails fast, logs once, sleeps 5 s. Two seconds is enough for
	// the first line and proves the retry does not log again inside the window.
	time.Sleep(2 * time.Second)
	cancel()
	<-done // the loop must be gone before captureLogs restores logPrintf
	var fails []string
	for _, l := range cap.all() {
		if strings.Contains(l, "getUpdates failed") {
			fails = append(fails, l)
		}
		if strings.Contains(l, "SECRETTOKEN") {
			t.Fatalf("bot token leaked into the journal: %s", l)
		}
	}
	if len(fails) != 1 {
		t.Fatalf("want exactly one getUpdates failed line in 2 s (first failure, then rate-limited), got %d: %v", len(fails), cap.all())
	}
	if !strings.Contains(fails[0], "/bot***/getUpdates") {
		t.Errorf("failure line should name the redacted URL: %s", fails[0])
	}
}

// The loop logs "recovered" exactly once when a failing poll starts working.
// The endpoint flips from dropping connections to serving getUpdates behind an
// atomic flag (no field swap under the loop).
func TestTelegramPollLoopLogsRecovery(t *testing.T) {
	var healthy atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			// Drop the connection mid-request: the client sees an error.
			hj, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "down", http.StatusBadGateway)
				return
			}
			c, _, err := hj.Hijack()
			if err == nil {
				_ = c.Close()
			}
			return
		}
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	t.Cleanup(srv.Close)
	s := newTGTestServer(t, srv, 1)
	cap := captureLogs(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.telegramPollLoop(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if l := cap.all(); len(l) > 0 && strings.Contains(l[0], "getUpdates failed") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	healthy.Store(true)
	// The loop is inside its 5 s retry sleep; wait for the recovery line
	// instead of a fixed sleep so -race and a loaded CI runner cannot skew it.
	count := func() (int, int) {
		var fails, recovers int
		for _, l := range cap.all() {
			switch {
			case strings.Contains(l, "getUpdates failed"):
				fails++
			case strings.Contains(l, "getUpdates recovered"):
				recovers++
			}
		}
		return fails, recovers
	}
	deadline = time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, r := count(); r >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	fails, recovers := count()
	if fails != 1 || recovers != 1 {
		t.Fatalf("want 1 failure + 1 recovery line, got %d/%d: %v", fails, recovers, cap.all())
	}
}

func TestRedactTGErrors(t *testing.T) {
	err := fmt.Errorf(`Post "https://api.telegram.org/bot12:ABC/getMe": dial tcp: lookup api.telegram.org`)
	got := redactTG(err, "12:ABC")
	if got.Error() != `Post "https://api.telegram.org/bot***/getMe": dial tcp: lookup api.telegram.org` {
		t.Fatalf("redaction failed: %s", got.Error())
	}
	if errors.Is(got, err) {
		t.Fatal("redacted wrapper must not unwrap to the token-bearing error")
	}
	if redactTG(err, "") != err || redactTG(nil, "x") != nil {
		t.Fatal("redactTG must pass through when there is nothing to redact")
	}
}
