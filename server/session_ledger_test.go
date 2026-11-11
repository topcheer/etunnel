package server

// Issue #69 acceptance: session ledger Tier 1 (journal lines) and Tier 2
// (PG rows, gated on ET_PG_TEST_DSN like account_pg_live_test.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/topcheer/etunnel/protocol"
)

type ledgerLogCap struct {
	mu    sync.Mutex
	lines []string
}

func (c *ledgerLogCap) printf(format string, a ...any) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, fmt.Sprintf(format, a...))
	return 0, nil
}

func (c *ledgerLogCap) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.lines...)
}

func dialTunnel(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/tunnel", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	hello, _ := json.Marshal(&protocol.Hello{Type: protocol.MsgHello, Proto: protocol.ProtoVersion})
	if err := ws.Write(ctx, websocket.MessageText, hello); err != nil {
		t.Fatalf("hello: %v", err)
	}
	_, raw, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("welcome: %v", err)
	}
	var w map[string]any
	if json.Unmarshal(raw, &w) != nil || w["type"] != protocol.MsgWelcome {
		t.Fatalf("handshake rejected: %s", raw)
	}
	return ws
}

func findLine(t *testing.T, lines []string, prefix string) string {
	t.Helper()
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	t.Fatalf("no journal line with prefix %q; got %v", prefix, lines)
	return ""
}

// TestSessionJournalOpenCloseLines: one tunnel round-trip must produce the
// paired open/close audit lines with the issue's stable field format.
func TestSessionJournalOpenCloseLines(t *testing.T) {
	cap := &ledgerLogCap{}
	old := logPrintf
	logPrintf = cap.printf
	t.Cleanup(func() { logPrintf = old })

	s, err := New(Config{
		Domain: "t.test", WSPath: "/tunnel",
		TLS:          TLSConfig{Mode: TLSModeHTTP},
		AccountsFile: filepath.Join(t.TempDir(), "a.json"),
		Tiers:        DefaultTiers(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 127.0.0.1 is not a base host; rewrite so /tunnel routes to the
		// real handshake path (same as the admission tests).
		r.Host = "t.test"
		s.Handler().ServeHTTP(w, r)
	}))
	defer ts.Close()

	ws := dialTunnel(t, ts)

	// open line: all fields, anonymous account, custom=false (logf adds the
	// "etunneld: " prefix; the default tier is Free).
	open := findLine(t, cap.all(), "etunneld: session ")
	if !strings.Contains(open, ": open ip=127.0.0.1 sub=") || !strings.Contains(open, " plan=Free account= custom=false") {
		t.Fatalf("open line malformed: %q", open)
	}
	f := strings.Fields(open)
	if len(f) < 3 {
		t.Fatalf("open line unparseable: %q", open)
	}
	sid := strings.TrimSuffix(f[2], ":")
	if sid == "" || strings.ContainsAny(sid, ":= ") {
		t.Fatalf("open line carries no session id: %q", open)
	}

	// Clean close; the server logs the paired close line.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = ws.Write(ctx, websocket.MessageText, mustJSON(t, &protocol.Bye{Type: protocol.MsgBye, Reason: "test done"}))
	_ = ws.Close(websocket.StatusNormalClosure, "done")

	deadline := time.Now().Add(5 * time.Second)
	var closeLine string
	for time.Now().Before(deadline) {
		for _, l := range cap.all() {
			if strings.Contains(l, ": close ip=") && strings.Contains(l, sid) {
				closeLine = l
			}
		}
		if closeLine != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if closeLine == "" {
		t.Fatalf("no close line for %s; journal: %v", sid, cap.all())
	}
	if !strings.Contains(closeLine, "dur=") || !strings.Contains(closeLine, "reason=") || !strings.Contains(closeLine, "streams=0") {
		t.Fatalf("close line missing fields: %q", closeLine)
	}
	// dur must parse as a duration and be sane (>= 0s, small).
	var dur time.Duration
	if _, err := fmt.Sscanf(closeLine, "session "+sid+": close ip=127.0.0.1 sub=%s", new(string)); err == nil {
		// full-field parse is brittle (sub values vary); scan dur= directly
	}
	i := strings.Index(closeLine, "dur=")
	if i < 0 {
		t.Fatal("no dur field")
	}
	rest := closeLine[i+4:]
	end := strings.IndexByte(rest, ' ')
	if end < 0 {
		end = len(rest)
	}
	dur, err = time.ParseDuration(rest[:end])
	if err != nil {
		t.Fatalf("dur %q unparseable: %v", rest[:end], err)
	}
	if dur < 0 || dur > time.Minute {
		t.Fatalf("dur out of sane range: %v", dur)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestSessionLedgerPGLifecycle: with a reachable ET_PG_TEST_DSN, an
// open/close round-trip writes and then updates the session row.
// Skipped without the DSN (repo test-compatibility rule).
func TestSessionLedgerPGLifecycle(t *testing.T) {
	dsn := os.Getenv("ET_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("ET_PG_TEST_DSN not set")
	}
	lg, err := newSessionLedger(dsn)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	defer lg.Close()

	sess := &Session{
		ip:   "203.0.113.9",
		sub:  "pgtest",
		tier: &Tier{Name: "pro"},
		id:   fmt.Sprintf("sess-pgtest-%d", time.Now().UnixNano()),
	}
	sess.started = time.Now()

	if err := lg.recordOpen(sess); err != nil {
		t.Fatalf("recordOpen: %v", err)
	}

	var ip, sub, plan, acct string
	var started time.Time
	var closed *time.Time
	err = lg.pool.QueryRow(context.Background(),
		`SELECT ip, sub, plan, account, started, closed FROM sessions WHERE id=$1`, sess.id).
		Scan(&ip, &sub, &plan, &acct, &started, &closed)
	if err != nil {
		t.Fatalf("row after open: %v", err)
	}
	if ip != sess.ip || sub != "pgtest" || plan != "pro" || acct != "" || closed != nil {
		t.Fatalf("open row wrong: ip=%s sub=%s plan=%s account=%q closed=%v", ip, sub, plan, acct, closed)
	}

	time.Sleep(1100 * time.Millisecond) // make duration_s >= 1 observable
	if err := lg.recordClose(sess, "test reason", 7); err != nil {
		t.Fatalf("recordClose: %v", err)
	}

	var dur int
	var reason string
	var streams int
	var closed2 time.Time
	err = lg.pool.QueryRow(context.Background(),
		`SELECT closed, duration_s, reason, streams FROM sessions WHERE id=$1`, sess.id).
		Scan(&closed2, &dur, &reason, &streams)
	if err != nil {
		t.Fatalf("row after close: %v", err)
	}
	if closed2.IsZero() || dur < 1 || reason != "test reason" || streams != 7 {
		t.Fatalf("close row wrong: closed=%v dur=%d reason=%q streams=%d", closed2, dur, reason, streams)
	}

	// housekeeping: the test row must not outlive the run
	_, _ = lg.pool.Exec(context.Background(), `DELETE FROM sessions WHERE id=$1`, sess.id)
}

// TestNilLedgerIsNoop: the no-DSN configuration calls the ledger methods on
// a nil receiver everywhere on the hot path; that must be a silent no-op.
func TestNilLedgerIsNoop(t *testing.T) {
	var lg *sessionLedger
	sess := &Session{id: "x", started: time.Now(), tier: &Tier{Name: "free"}}
	if err := lg.recordOpen(sess); err != nil {
		t.Fatalf("nil recordOpen: %v", err)
	}
	if err := lg.recordClose(sess, "r", 0); err != nil {
		t.Fatalf("nil recordClose: %v", err)
	}
	lg.Close() // must not panic
}
