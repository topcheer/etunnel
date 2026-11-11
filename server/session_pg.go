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

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// sessionLedger records session open/close rows in Postgres. A nil ledger
// (the no-DSN configuration) records nothing and fails nowhere.
type sessionLedger struct {
	pool *pgxpool.Pool
}

func newSessionLedger(dsn string) (*sessionLedger, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("etunnel: bad ETUNNEL_PG_DSN (sessions): %w", err)
	}
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("etunnel: sessions pg connect: %w", err)
	}
	if _, err := pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS sessions (
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
)`); err != nil {
		pool.Close()
		return nil, fmt.Errorf("etunnel: sessions migrate: %w", err)
	}
	return &sessionLedger{pool: pool}, nil
}

// recordOpen inserts the session row (idempotent on the PK: a server restart
// re-runs opens only for new sessions, but a re-insert is an UPDATE no-op).
func (l *sessionLedger) recordOpen(sess *Session) error {
	if l == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := l.pool.Exec(ctx,
		`INSERT INTO sessions (id, ip, sub, plan, account, started)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (id) DO NOTHING`,
		sess.id, sess.ip, sess.sub, sess.tier.Name, sess.accountEmail(), sess.started)
	return err
}

// recordClose fills the closed/duration/reason/streams columns of the row.
func (l *sessionLedger) recordClose(sess *Session, reason string, streams int) error {
	if l == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	dur := time.Since(sess.started)
	_, err := l.pool.Exec(ctx,
		`UPDATE sessions SET closed = $2, duration_s = $3, reason = $4, streams = $5
		 WHERE id = $1`,
		sess.id, time.Now(), int(dur.Seconds()), reason, streams)
	return err
}

func (l *sessionLedger) Close() {
	if l != nil && l.pool != nil {
		l.pool.Close()
	}
}
