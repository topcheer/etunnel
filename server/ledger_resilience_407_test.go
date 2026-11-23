package server

// Issue #407: PG session ledger resilience. Before #407 a startup PG
// failure returned an error from Run (process exit, crash loop) and every
// write had a 3s budget that the lossy prod link blew (76 lost rows/24h).
//
// These tests pin the new contract:
//   - startup failure degrades to journal-only (no exit) with a loud warn,
//     /stats stays reachable and reports the mode, and a background retry
//     switches the ledger back to PG mode when PG returns;
//   - -ledger-strict restores the fail-closed error;
//   - write ctx and pool sizing match the values the review settled on;
//   - failures are counted exactly and logged sampled (<=1/min).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// closedPortDSN returns a DSN pointing at a localhost port that was bound
// and released: connections to it are refused immediately, which is how a
// test simulates "PG unreachable" without a live server.
func closedPortDSN(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return "postgres://u:p@" + addr + "/db?sslmode=disable"
}

// lazyPool builds a real *pgxpool.Pool object without contacting a server
// (pgx connects lazily). The #407 recovery test uses it as the "PG is back"
// stand-in: the ledger only needs a pool to flip into PG mode.
func lazyPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	cfg, err := ledgerPoolConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("lazy pool: %v", err)
	}
	return p
}

func waitFor407(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func countLines(lines []string, substr string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, substr) {
			n++
		}
	}
	return n
}

func ledgerTestSession(id string) *Session {
	s := &Session{id: id, ip: "203.0.113.7", sub: "l407", tier: &Tier{Name: "free"}}
	s.started = time.Now()
	return s
}

// statsJSON drives handleStats directly (in-package) and decodes the body.
func statsJSON(t *testing.T, s *Server) map[string]any {
	t.Helper()
	rr := httptest.NewRecorder()
	s.handleStats(rr, httptest.NewRequest(http.MethodGet, "/stats", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/stats status %d", rr.Code)
	}
	var m map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("/stats body: %v", err)
	}
	return m
}

// ③ Pool sizing and write budget (#407 item 2).
func TestLedger407PoolConfigAndTimeouts(t *testing.T) {
	cfg, err := ledgerPoolConfig("postgres://u:p@db.example:5432/x?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConns != 4 || cfg.MinConns != 1 {
		t.Fatalf("pool conns: Max=%d Min=%d, want 4/1", cfg.MaxConns, cfg.MinConns)
	}
	if cfg.MaxConnLifetime != 30*time.Minute {
		t.Fatalf("MaxConnLifetime %s, want 30m", cfg.MaxConnLifetime)
	}
	if cfg.HealthCheckPeriod != time.Minute {
		t.Fatalf("HealthCheckPeriod %s, want 1m", cfg.HealthCheckPeriod)
	}
	if ledgerWriteTimeout != 10*time.Second {
		t.Fatalf("ledgerWriteTimeout %s, want 10s (was 3s before #407)", ledgerWriteTimeout)
	}
	if ledgerInitTimeout != 10*time.Second || ledgerRetryInterval != 30*time.Second {
		t.Fatalf("init=%s retry=%s, want 10s/30s", ledgerInitTimeout, ledgerRetryInterval)
	}
	if _, err := ledgerPoolConfig("://not a dsn"); err == nil {
		t.Fatal("bad dsn accepted")
	}
}

// ① Startup with PG unreachable: no error (no exit), loud warn, /stats
// reachable in journal-only mode, degraded writes counted + sampled, then
// the background retry brings the ledger back to PG mode.
func TestLedger407StartupDegradesThenRecovers(t *testing.T) {
	dsn := closedPortDSN(t)
	cap := &ledgerLogCap{}
	lgf := func(f string, a ...any) { cap.printf(f, a...) }

	lg, err := newSessionLedger(dsn, false, lgf)
	if err != nil {
		t.Fatalf("default mode must NOT fail startup on PG outage (#407): %v", err)
	}
	t.Cleanup(lg.Close)
	if got := lg.mode(); got != "journal-only" {
		t.Fatalf("mode %q, want journal-only", got)
	}
	lines := cap.all()
	if countLines(lines, "WARN session ledger") != 1 || countLines(lines, "DEGRADED to journal-only") != 1 ||
		!strings.Contains(strings.Join(lines, "\n"), "will NOT be written to PG") {
		t.Fatalf("missing loud degrade warn; log=%q", lines)
	}

	// /stats stays reachable and reports the degraded mode.
	s := &Server{reg: newRegistry(), metrics: &metrics{}, ledger: lg}
	m := statsJSON(t, s)
	if m["ledger_mode"] != "journal-only" || m["ledger_failed_total"] != float64(0) || m["ledger_reconnects"] != float64(0) {
		t.Fatalf("degraded /stats ledger fields: %v %v %v", m["ledger_mode"], m["ledger_failed_total"], m["ledger_reconnects"])
	}

	// Degraded writes: every one is a lost audit row (counted), but the log
	// is sampled to one line per minute.
	for i := 0; i < 5; i++ {
		if err := lg.recordOpen(ledgerTestSession(fmt.Sprintf("s%d", i))); !errors.Is(err, errLedgerDegraded) {
			t.Fatalf("degraded recordOpen #%d err=%v, want errLedgerDegraded", i, err)
		}
	}
	if n := lg.failedTotal(); n != 5 {
		t.Fatalf("failed_total %d, want 5", n)
	}
	if got := countLines(cap.all(), "open failed for session"); got != 1 {
		t.Fatalf("failure logs %d, want exactly 1 sampled line", got)
	}
	if m = statsJSON(t, s); m["ledger_failed_total"] != float64(5) {
		t.Fatalf("/stats ledger_failed_total %v, want 5", m["ledger_failed_total"])
	}

	// Recovery: PG is still down on the first retry, then comes back.
	var attempts atomic.Int32
	lg.connectFn = func(ctx context.Context) (*pgxpool.Pool, error) {
		if attempts.Add(1) == 1 {
			return nil, errors.New("connection refused (simulated)")
		}
		return lazyPool(t, dsn), nil
	}
	lg.retryInterval = 10 * time.Millisecond
	lg.startRetry(context.Background())
	waitFor407(t, "ledger to recover into PG mode", func() bool { return lg.mode() == "pg" })

	if n := lg.reconnectsTotal(); n != 1 {
		t.Fatalf("reconnects %d, want 1", n)
	}
	lines = cap.all()
	if countLines(lines, "PG still unavailable") != 1 {
		t.Fatalf("expected one 'still unavailable' line for the failed retry; log=%q", lines)
	}
	if countLines(lines, "RECOVERED") != 1 {
		t.Fatalf("expected RECOVERED line; log=%q", lines)
	}
	if m = statsJSON(t, s); m["ledger_mode"] != "pg" || m["ledger_reconnects"] != float64(1) {
		t.Fatalf("recovered /stats: mode=%v reconnects=%v", m["ledger_mode"], m["ledger_reconnects"])
	}

	// The retry loop has exited after recovery: no further attempts.
	time.Sleep(50 * time.Millisecond)
	if a := attempts.Load(); a != 2 {
		t.Fatalf("retry loop kept running after recovery: %d attempts", a)
	}

	// In PG mode a write that cannot reach the DB is a real failure (not
	// degraded): still counted, still sampled.
	if err := lg.recordOpen(ledgerTestSession("after")); err == nil || errors.Is(err, errLedgerDegraded) {
		t.Fatalf("pg-mode write against closed port err=%v, want a connection error", err)
	}
	if n := lg.failedTotal(); n != 6 {
		t.Fatalf("failed_total %d, want 6", n)
	}

	// Close: pool dropped, loop stopped, idempotent, startRetry is a no-op.
	lg.Close()
	lg.Close()
	if got := lg.mode(); got != "journal-only" {
		t.Fatalf("after Close mode %q, want journal-only", got)
	}
	lg.startRetry(context.Background()) // must not spawn after Close (startOnce)
}

// blackholeDSN listens, accepts and never answers: pgx's TCP connect
// succeeds and its startup message then waits for the whole ctx budget. That
// gives a PG-mode ledger whose writes hang, so any caller-side blocking is
// observable.
func blackholeDSN(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
	})
	return "postgres://u:p@" + ln.Addr().String() + "/db?sslmode=disable"
}

// #407 item 4: the session path must never wait on PG. With a 10s write
// budget a synchronous write on a stalled link would hold the handshake out
// of its serve loop - and hold the subdomain slot on close - for 10s.
func TestLedger407AsyncWritesNeverBlockCaller(t *testing.T) {
	// Degraded: the wrapper returns at once and the lost row is still counted.
	lg, err := newSessionLedger(closedPortDSN(t), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lg.Close)
	t0 := time.Now()
	lg.recordOpenAsync(ledgerTestSession("a1"))
	if d := time.Since(t0); d > 500*time.Millisecond {
		t.Fatalf("degraded recordOpenAsync blocked the caller %s", d)
	}
	waitFor407(t, "degraded async write to be counted", func() bool { return lg.failedTotal() == 1 })

	// PG mode against a blackhole: the writes hang for the full 10s budget,
	// yet handshake (open) and close path (close) must return immediately.
	// Built directly (not via newSessionLedger) so startup does not itself
	// wait on the blackhole; lazyPool performs no I/O at construction.
	bh := blackholeDSN(t)
	lg2 := &sessionLedger{dsn: bh, retryInterval: ledgerRetryInterval, stop: make(chan struct{}), pool: lazyPool(t, bh)}
	lg2.logf = func(string, ...any) {}
	t0 = time.Now()
	lg2.recordOpenAsync(ledgerTestSession("a2"))
	lg2.recordCloseAsync(ledgerTestSession("a3"), "done", 2)
	if d := time.Since(t0); d > 500*time.Millisecond {
		t.Fatalf("PG-mode async writes blocked the caller %s - writes must be off the handshake/close path", d)
	}
	// Not lg2.Close(): pool.Close would wait for the blackholed Exec (10s).
	// Detach instead; the stuck goroutines end with their own ctx.
	lg2.stopOnce.Do(func() { close(lg2.stop) })
	lg2.mu.Lock()
	lg2.pool = nil
	lg2.mu.Unlock()

	// nil ledger: no goroutine, no panic.
	var none *sessionLedger
	none.recordOpenAsync(ledgerTestSession("n"))
	none.recordCloseAsync(ledgerTestSession("n"), "x", 0)
}

// ② -ledger-strict: the pre-#407 fail-closed behavior is still available.
func TestLedger407StrictFailClosed(t *testing.T) {
	dsn := closedPortDSN(t)
	cap := &ledgerLogCap{}
	lgf := func(f string, a ...any) { cap.printf(f, a...) }

	lg, err := newSessionLedger(dsn, true, lgf)
	if err == nil || lg != nil {
		t.Fatalf("strict mode must fail closed: lg=%v err=%v", lg, err)
	}
	if !strings.Contains(err.Error(), "-ledger-strict") || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("strict error should name the flag and the cause: %v", err)
	}
	if n := countLines(cap.all(), "DEGRADED"); n != 0 {
		t.Fatalf("strict mode must not claim a degrade; log=%q", cap.all())
	}
}

// Server wiring: setupLedger is what Run calls. Strict -> error (Run aborts);
// default -> degraded ledger and nil error (process keeps serving); no DSN
// -> no ledger.
func TestLedger407SetupLedgerWiring(t *testing.T) {
	ctx := context.Background()
	dsn := closedPortDSN(t)

	s := &Server{cfg: Config{LedgerStrict: true}}
	t.Setenv("ETUNNEL_PG_DSN", dsn)
	if err := s.setupLedger(ctx); err == nil {
		t.Fatal("strict wiring: setupLedger must return the error")
	}
	if s.ledger != nil {
		t.Fatal("strict wiring: no ledger must be installed")
	}

	s = &Server{cfg: Config{}}
	if err := s.setupLedger(ctx); err != nil {
		t.Fatalf("default wiring: PG outage must not fail startup: %v", err)
	}
	if s.ledger == nil || s.ledger.mode() != "journal-only" {
		t.Fatalf("default wiring: want a degraded ledger, got %v", s.ledger)
	}
	s.ledger.Close()
	if s.ledger.mode() != "journal-only" {
		t.Fatal("wiring: Close must drop the pool")
	}

	t.Setenv("ETUNNEL_PG_DSN", "")
	s = &Server{cfg: Config{LedgerStrict: true}}
	if err := s.setupLedger(ctx); err != nil {
		t.Fatalf("no DSN: %v", err)
	}
	if s.ledger != nil {
		t.Fatal("no DSN: ledger must stay nil")
	}
	// No-DSN /stats still carries the fields ("off"), and a nil ledger is a
	// no-op for writes and Close.
	m := statsJSON(t, &Server{reg: newRegistry(), metrics: &metrics{}})
	if m["ledger_mode"] != "off" || m["ledger_failed_total"] != float64(0) || m["ledger_reconnects"] != float64(0) {
		t.Fatalf("no-DSN /stats ledger fields: %v", m)
	}
	var nilLedger *sessionLedger
	if err := nilLedger.recordOpen(ledgerTestSession("nil")); err != nil {
		t.Fatal(err)
	}
	nilLedger.startRetry(ctx)
	nilLedger.Close()
	if nilLedger.mode() != "off" {
		t.Fatal("nil ledger mode")
	}
}
