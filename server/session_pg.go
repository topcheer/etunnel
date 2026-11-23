package server

// Session ledger (issue #69 Tier 2): an optional Postgres audit trail of
// tunnel sessions. Enabled with the same ETUNNEL_PG_DSN as the account store
// (independent of cluster mode); without a DSN the field stays nil and the
// journal logs (Tier 1) are the only record - exactly the previous behavior.
//
// Schema (one table, auto-migrated like the accounts table):
//
//	CREATE TABLE IF NOT EXISTS sessions (
//	  id          TEXT PRIMARY KEY,   -- the session id from the hello
//	  ip          TEXT,
//	  sub         TEXT,               -- subdomain label
//	  plan        TEXT,
//	  account     TEXT,               -- '' for anonymous tunnels
//	  started     TIMESTAMPTZ NOT NULL,
//	  closed      TIMESTAMPTZ,        -- NULL while open
//	  duration_s  INT,
//	  reason      TEXT,               -- the Close reason
//	  streams     INT                 -- streams served over the session
//	);
//
// Retention: NO automatic cleanup - the table is archived by ops on demand
// (see PACKAGING.md). This is an audit ledger, not a metrics store.
//
// Writes are best-effort on the session path: a ledger failure logs and the
// session proceeds (an audit sink must never take tunnels down).
//
// Resilience (#407): in production Postgres is a remote dependency (RTT
// ~218ms, 10-13% packet loss, TCP handshake p95 1.25s). Before #407 a startup
// connect/migrate failure made etunneld exit (crash loop), and the 3s write
// budget was regularly blown, losing audit rows (76 in 24h observed). Now:
//
//   - a startup failure DEGRADES to journal-only instead of exiting, logs a
//     loud warning, and a background loop retries every 30s; on success the
//     ledger switches back to PG mode. -ledger-strict restores the old
//     fail-closed behavior for operators who want it;
//   - writes get a 10s budget and the pool is sized for a lossy link
//     (MaxConns 4 / MinConns 1 / MaxConnLifetime 30m / HealthCheck 1m);
//   - failed writes and successful reconnects are counted atomically and
//     exposed on /stats as ledger_failed_total / ledger_reconnects; failure
//     logs are sampled to one line per minute so an outage cannot flood
//     journald.
//
// The session path keeps its non-blocking semantics: writes are best-effort
// calls made off the handshake's critical decisions and never alter the
// outcome of a connect or close.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Ledger timings and pool sizing (#407). Package constants so tests can pin
// them to the values the incident review settled on.
const (
	// ledgerInitTimeout bounds connect + CREATE TABLE at startup and on every
	// background retry. 10s absorbs a 1.25s-p95 handshake with retransmits.
	ledgerInitTimeout = 10 * time.Second
	// ledgerWriteTimeout bounds each recordOpen / recordClose / recentOpens
	// statement. Was 3s: one retransmit plus the TLS/db round trips on the
	// prod link could exceed it. 10s matches the init budget; the call is
	// best-effort and off the handshake's critical path either way.
	ledgerWriteTimeout = 10 * time.Second
	// ledgerRetryInterval is the delay between background reconnect attempts
	// while the ledger is degraded.
	ledgerRetryInterval = 30 * time.Second
	// ledgerFailLogInterval is the sampling window for write-failure logs.
	ledgerFailLogInterval = time.Minute
)

// ledgerPoolConfig parses dsn and applies the #407 pool sizing. Pure (no I/O)
// so the sizing is unit-testable without a server.
func ledgerPoolConfig(dsn string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 4
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.HealthCheckPeriod = time.Minute
	return cfg, nil
}

// errLedgerDegraded is returned by writes while the ledger has no PG pool
// (journal-only after a startup failure, before the background retry
// recovers). It is counted as a failed write: the audit row was lost.
var errLedgerDegraded = errors.New("etunnel: session ledger degraded to journal-only (PG unavailable, retrying in background)")

// sessionLedger records session open/close rows in Postgres. A nil ledger
// (the no-DSN configuration) records nothing and fails nowhere.
//
// Concurrency: pool is guarded by mu because the background retry loop swaps
// it in while session goroutines read it. No network call is ever made while
// holding mu - writers copy the pointer under RLock and Exec on it; Close and
// the retry loop close pools after releasing the lock.
type sessionLedger struct {
	dsn    string
	strict bool
	logf   func(format string, args ...any) // never nil; defaults to logPrintf

	mu   sync.RWMutex
	pool *pgxpool.Pool // nil = degraded (journal-only), #407

	// connectFn overrides connect for tests (simulate PG going away and
	// coming back without a live server). nil = the real constructor.
	connectFn func(ctx context.Context) (*pgxpool.Pool, error)

	retryInterval time.Duration // default ledgerRetryInterval; tests shorten it
	startOnce     sync.Once
	stop          chan struct{}
	stopOnce      sync.Once

	// Observability (#407): atomic counters surfaced on /stats.
	failed     atomic.Int64 // writes that did not land (including degraded)
	reconnects atomic.Int64 // background retries that restored PG mode

	logMu       sync.Mutex
	lastFailLog time.Time // sampling gate for write-failure logs
}

// newSessionLedger connects to PG and migrates the sessions table.
//
// On failure: strict=true returns (nil, err) so the caller aborts startup
// (the pre-#407 fail-closed behavior, opt-in via -ledger-strict);
// strict=false returns a DEGRADED ledger (pool == nil, journal-only) with a
// nil error - the caller must then call startRetry so the background loop
// keeps trying and switches the ledger back to PG mode when PG returns.
func newSessionLedger(dsn string, strict bool, logf func(string, ...any)) (*sessionLedger, error) {
	l := &sessionLedger{
		dsn:           dsn,
		strict:        strict,
		logf:          logf,
		retryInterval: ledgerRetryInterval,
		stop:          make(chan struct{}),
	}
	if l.logf == nil {
		l.logf = func(format string, args ...any) { logPrintf("etunneld: "+format+"\n", args...) }
	}
	ctx, cancel := context.WithTimeout(context.Background(), ledgerInitTimeout)
	defer cancel()
	pool, err := l.connector()(ctx)
	if err != nil {
		if strict {
			return nil, fmt.Errorf("etunnel: sessions ledger (-ledger-strict): %w", err)
		}
		l.logf("WARN session ledger: PG unavailable (%v); DEGRADED to journal-only - audit rows will NOT be written to PG until it recovers (retrying every %s; -ledger-strict restores fail-closed)", err, l.retryInterval)
		return l, nil
	}
	l.pool = pool
	return l, nil
}

// connector returns the pool constructor: the test seam when set, else the
// real one.
func (l *sessionLedger) connector() func(context.Context) (*pgxpool.Pool, error) {
	if l.connectFn != nil {
		return l.connectFn
	}
	return l.connect
}

// connect builds the pool and migrates the schema. It does not mutate l, so
// the retry loop can run it while session goroutines keep using the current
// (nil) pool.
func (l *sessionLedger) connect(ctx context.Context) (*pgxpool.Pool, error) {
	cfg, err := ledgerPoolConfig(l.dsn)
	if err != nil {
		return nil, fmt.Errorf("etunnel: bad ETUNNEL_PG_DSN (sessions): %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("etunnel: sessions pg connect: %w", err)
	}
	if _, err := pool.Exec(ctx, sessionsSchema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("etunnel: sessions migrate: %w", err)
	}
	return pool, nil
}

const sessionsSchema = `CREATE TABLE IF NOT EXISTS sessions (
  id         TEXT PRIMARY KEY,
  ip         TEXT,
  sub        TEXT,
  plan       TEXT,
  account    TEXT,
  started    TIMESTAMPTZ NOT NULL,
  closed     TIMESTAMPTZ,
  duration_s INT,
  reason     TEXT,
  streams    INT
)`

// startRetry launches the background reconnect loop if the ledger is
// degraded. Idempotent; a no-op when the pool is already up. The loop exits
// when PG recovers (switching the ledger back to PG mode), when Close is
// called, or when ctx is done.
func (l *sessionLedger) startRetry(ctx context.Context) {
	if l == nil {
		return
	}
	l.mu.RLock()
	up := l.pool != nil
	l.mu.RUnlock()
	if up {
		return
	}
	l.startOnce.Do(func() {
		go l.retryLoop(ctx)
	})
}

// retryLoop is the #407 background reconnect: every retryInterval, try to
// build the pool + schema; on success swap it in and stop. A failure logs
// once per attempt - bounded by construction (one line per 30s), so no
// extra sampling is needed here.
func (l *sessionLedger) retryLoop(ctx context.Context) {
	t := time.NewTicker(l.retryInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.stop:
			return
		case <-t.C:
		}
		ictx, cancel := context.WithTimeout(ctx, ledgerInitTimeout)
		pool, err := l.connector()(ictx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			l.logf("session ledger: PG still unavailable, next retry in %s: %v", l.retryInterval, err)
			continue
		}
		// Swap in unless Close raced us: Close sets pool=nil and closes stop
		// under mu, so checking stop while holding mu makes the hand-off
		// leak-free whichever side gets there first.
		l.mu.Lock()
		aborted := false
		select {
		case <-l.stop:
			aborted = true
		default:
			l.pool = pool
		}
		l.mu.Unlock()
		if aborted {
			pool.Close()
			return
		}
		n := l.reconnects.Add(1)
		l.logf("session ledger: RECOVERED - PG mode restored after %d background reconnect(s); audit rows resume", n)
		return
	}
}

// live returns the current pool, or errLedgerDegraded while journal-only.
func (l *sessionLedger) live() (*pgxpool.Pool, error) {
	l.mu.RLock()
	p := l.pool
	l.mu.RUnlock()
	if p == nil {
		return nil, errLedgerDegraded
	}
	return p, nil
}

// noteFailure counts a lost audit row and logs it, sampled to at most one
// line per ledgerFailLogInterval (#407: a PG outage used to emit one line
// per session close and flood journald). The count is exact even when the
// log is suppressed - /stats carries it.
func (l *sessionLedger) noteFailure(op, id string, err error) {
	n := l.failed.Add(1)
	l.logMu.Lock()
	sample := time.Since(l.lastFailLog) >= ledgerFailLogInterval
	if sample {
		l.lastFailLog = time.Now()
	}
	l.logMu.Unlock()
	if sample {
		l.logf("WARN session ledger: %s failed for session %s (total %d; further failures sampled for %s): %v",
			op, id, n, ledgerFailLogInterval, err)
	}
}

// mode reports the ledger state for /stats: "off" (no DSN), "journal-only"
// (degraded, retrying) or "pg".
func (l *sessionLedger) mode() string {
	if l == nil {
		return "off"
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.pool == nil {
		return "journal-only"
	}
	return "pg"
}

func (l *sessionLedger) failedTotal() int64 {
	if l == nil {
		return 0
	}
	return l.failed.Load()
}

func (l *sessionLedger) reconnectsTotal() int64 {
	if l == nil {
		return 0
	}
	return l.reconnects.Load()
}

// recordOpen inserts the session row (idempotent on the PK: a server restart
// re-runs opens only for new sessions, but a re-insert is an UPDATE no-op).
// Errors are returned for callers/tests; the ledger has already counted the
// loss and emitted a sampled log line (#407).
func (l *sessionLedger) recordOpen(sess *Session) error {
	if l == nil {
		return nil
	}
	pool, err := l.live()
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), ledgerWriteTimeout)
		defer cancel()
		_, err = pool.Exec(ctx,
			`INSERT INTO sessions (id, ip, sub, plan, account, started)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 ON CONFLICT (id) DO NOTHING`,
			sess.id, sess.ip, sess.sub, sess.tier.Name, sess.accountEmail(), sess.started)
	}
	if err != nil {
		l.noteFailure("open", sess.id, err)
	}
	return err
}

// recordClose fills the closed/duration/reason/streams columns of the row.
func (l *sessionLedger) recordClose(sess *Session, reason string, streams int) error {
	if l == nil {
		return nil
	}
	pool, err := l.live()
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), ledgerWriteTimeout)
		defer cancel()
		dur := time.Since(sess.started)
		_, err = pool.Exec(ctx,
			`UPDATE sessions SET closed = $2, duration_s = $3, reason = $4, streams = $5
			 WHERE id = $1`,
			sess.id, time.Now(), int(dur.Seconds()), reason, streams)
	}
	if err != nil {
		l.noteFailure("close", sess.id, err)
	}
	return err
}

// Close stops the retry loop and closes the pool. Safe on a nil ledger and
// idempotent. The pool is closed after releasing mu so a slow Close (it
// waits for in-flight connections, bounded by their write ctx) never blocks
// session writers.
func (l *sessionLedger) Close() {
	if l == nil {
		return
	}
	l.stopOnce.Do(func() { close(l.stop) })
	l.mu.Lock()
	p := l.pool
	l.pool = nil
	l.mu.Unlock()
	if p != nil {
		p.Close()
	}
}

// recentOpens aggregates tunnel establishments inside the window (the
// /history admin command): total, distinct source IPs, the top IPs and the
// newest rows. nil-safe for non-PG deployments (caller falls back); while
// degraded it reports errLedgerDegraded so the caller falls back the same
// way. Reads are not counted as failed writes.
func (l *sessionLedger) recentOpens(window time.Duration) (total int, byIP map[string]int, newest []string, err error) {
	if l == nil {
		return 0, nil, nil, fmt.Errorf("etunnel: no session ledger")
	}
	pool, err := l.live()
	if err != nil {
		return 0, nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), ledgerWriteTimeout)
	defer cancel()
	if err = pool.QueryRow(ctx,
		`SELECT count(*), count(DISTINCT ip) FROM sessions WHERE started > now() - $1::interval`,
		window.String()).Scan(&total, new(int)); err != nil {
		return 0, nil, nil, err
	}
	byIP = map[string]int{}
	rows, err := pool.Query(ctx,
		`SELECT ip, count(*) c FROM sessions WHERE started > now() - $1::interval GROUP BY ip ORDER BY c DESC LIMIT 5`,
		window.String())
	if err != nil {
		return 0, nil, nil, err
	}
	for rows.Next() {
		var ip string
		var c int
		if err = rows.Scan(&ip, &c); err != nil {
			rows.Close()
			return 0, nil, nil, err
		}
		byIP[ip] = c
	}
	rows.Close()
	rows, err = pool.Query(ctx,
		`SELECT to_char(started, 'HH24:MI:SS'), ip, sub, plan FROM sessions WHERE started > now() - $1::interval ORDER BY started DESC LIMIT 5`,
		window.String())
	if err != nil {
		return 0, nil, nil, err
	}
	for rows.Next() {
		var ts, ip, sub, plan string
		if err = rows.Scan(&ts, &ip, &sub, &plan); err != nil {
			rows.Close()
			return 0, nil, nil, err
		}
		newest = append(newest, fmt.Sprintf("%s %s [%s] ip %s", ts, sub, plan, ip))
	}
	rows.Close()
	return total, byIP, newest, rows.Err()
}
