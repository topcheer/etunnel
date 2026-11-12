package server

// Postgres AccountStore: the cluster-mode account backend. Enabled with
// ETUNNEL_PG_DSN (postgres://user:pass@host:5432/dbname); without it the
// JSON-file store keeps working exactly as before.
//
// Schema (one table, created on first use):
//
//	CREATE TABLE IF NOT EXISTS accounts (
//	  api_key   TEXT PRIMARY KEY,
//	  email     TEXT NOT NULL UNIQUE,
//	  tier      TEXT NOT NULL,
//	  created   TIMESTAMPTZ NOT NULL,
//	  provider  TEXT NOT NULL DEFAULT '',
//	  subject   TEXT NOT NULL DEFAULT '',
//	  name      TEXT NOT NULL DEFAULT '',
//	  domains   TEXT[] NOT NULL DEFAULT '{}',
//	  expires_at TIMESTAMPTZ,   -- NULL = no expiry
//	  lapsed_at  TIMESTAMPTZ,   -- NULL = never paid
//	  last_seen  TIMESTAMPTZ
//	);
//
// The store keeps the same in-memory maps as the JSON store (hot path stays
// lock-free reads) and writes through to Postgres on every mutation. On a
// multi-instance cluster each node caches all accounts and refreshes on a
// short poll (ETUNNEL_PG_POLL, default 5s) plus an explicit Reload used by
// the gossip layer when a peer signals a change.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
// ErrEmailTaken mirrors the JSON store's sentinel.
// (Reused from account.go - same value.)
// ErrEmailTaken = errors.New(...)
)

type pgAccountStore struct {
	mu     sync.RWMutex
	byKey  map[string]*Account
	byMail map[string]*Account
	pool   *pgxpool.Pool
	poll   time.Duration
	cancel context.CancelFunc
}

func newPgAccountStore(dsn string) (*pgAccountStore, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("etunnel: bad ETUNNEL_PG_DSN: %w", err)
	}
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("etunnel: pg connect: %w", err)
	}
	s := &pgAccountStore{
		byKey:  map[string]*Account{},
		byMail: map[string]*Account{},
		pool:   pool,
	}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := s.loadAll(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	poll := 5 * time.Second
	pctx, pcancel := context.WithCancel(context.Background())
	s.cancel = pcancel
	s.poll = poll
	go s.pollLoop(pctx)
	return s, nil
}

func (s *pgAccountStore) migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS accounts (
  api_key    TEXT PRIMARY KEY,
  email      TEXT NOT NULL UNIQUE,
  tier       TEXT NOT NULL,
  created    TIMESTAMPTZ NOT NULL,
  provider   TEXT NOT NULL DEFAULT '',
  subject    TEXT NOT NULL DEFAULT '',
  name       TEXT NOT NULL DEFAULT '',
  domains    TEXT[] NOT NULL DEFAULT '{}',
  expires_at TIMESTAMPTZ,
  lapsed_at  TIMESTAMPTZ,
  last_seen  TIMESTAMPTZ
)`)
	return err
}

func (s *pgAccountStore) loadAll(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT api_key, email, tier, created, provider, subject, name, domains, expires_at, lapsed_at FROM accounts ORDER BY created`)
	if err != nil {
		return err
	}
	defer rows.Close()
	byKey := map[string]*Account{}
	byMail := map[string]*Account{}
	for rows.Next() {
		a := &Account{Domains: []string{}}
		var exp, lap *time.Time
		if err := rows.Scan(&a.APIKey, &a.Email, &a.Tier, &a.Created, &a.Provider, &a.Subject, &a.Name, &a.Domains, &exp, &lap); err != nil {
			return err
		}
		if exp != nil {
			a.ExpiresAt = *exp
		}
		if lap != nil {
			a.LapsedAt = *lap
		}
		byKey[a.APIKey] = a
		byMail[a.Email] = a
	}
	s.mu.Lock()
	s.byKey, s.byMail = byKey, byMail
	s.mu.Unlock()
	return rows.Err()
}

func (s *pgAccountStore) pollLoop(ctx context.Context) {
	t := time.NewTicker(s.poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = s.loadAll(ctx)
		}
	}
}

// upsert writes one account row.
func (s *pgAccountStore) upsert(ctx context.Context, a *Account) error {
	var exp, lap any
	if !a.ExpiresAt.IsZero() {
		exp = a.ExpiresAt
	}
	if !a.LapsedAt.IsZero() {
		lap = a.LapsedAt
	}
	_, err := s.pool.Exec(ctx, `
INSERT INTO accounts (api_key, email, tier, created, provider, subject, name, domains, expires_at, lapsed_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
ON CONFLICT (api_key) DO UPDATE SET
  email=EXCLUDED.email, tier=EXCLUDED.tier, provider=EXCLUDED.provider,
  subject=EXCLUDED.subject, name=EXCLUDED.name, domains=EXCLUDED.domains,
  expires_at=EXCLUDED.expires_at, lapsed_at=EXCLUDED.lapsed_at`,
		a.APIKey, a.Email, a.Tier, a.Created, a.Provider, a.Subject, a.Name, a.Domains, exp, lap)
	return err
}

// Create implements the AccountStore surface used by the server.
func (s *pgAccountStore) Create(email, tier string) (*Account, error) {
	s.mu.Lock()
	if _, ok := s.byMail[email]; ok {
		s.mu.Unlock()
		return nil, ErrEmailTaken
	}
	s.mu.Unlock()
	a := &Account{APIKey: newAPIKey(), Email: email, Tier: tier, Created: time.Now().UTC(), Domains: []string{}}
	if err := s.upsert(context.Background(), a); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.byKey[a.APIKey] = a
	s.byMail[a.Email] = a
	s.mu.Unlock()
	return a, nil
}

func (s *pgAccountStore) Lookup(key string) (*Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.byKey[key]
	return a, ok
}

func (s *pgAccountStore) LookupEmail(email string) (*Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.byMail[email]
	return a, ok
}

func (s *pgAccountStore) FindByProvider(provider, subject string) (*Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.byKey {
		if a.Provider == provider && a.Subject == subject {
			return a, true
		}
	}
	return nil, false
}

func (s *pgAccountStore) Delete(key string) bool {
	s.mu.Lock()
	a, ok := s.byKey[key]
	if !ok {
		s.mu.Unlock()
		return false
	}
	delete(s.byKey, key)
	delete(s.byMail, a.Email)
	s.mu.Unlock()
	_, _ = s.pool.Exec(context.Background(), `DELETE FROM accounts WHERE api_key=$1`, key)
	return true
}

func (s *pgAccountStore) ResetKey(email string) (string, bool) {
	s.mu.Lock()
	a, ok := s.byMail[email]
	if !ok {
		s.mu.Unlock()
		return "", false
	}
	oldKey := a.APIKey
	a.APIKey = newAPIKey()
	delete(s.byKey, oldKey)
	s.byKey[a.APIKey] = a
	s.mu.Unlock()
	_, _ = s.pool.Exec(context.Background(), `UPDATE accounts SET api_key=$1 WHERE email=$2`, a.APIKey, email)
	return a.APIKey, true
}

func (s *pgAccountStore) CancelSubscription(email string) bool {
	return s.Mutate(email, func(a *Account) { a.Tier = "free"; a.ExpiresAt = time.Time{} })
}

// DowngradeToFree keeps the lapsed marker like the JSON store.
func (s *pgAccountStore) DowngradeToFree(email string) bool {
	return s.Mutate(email, func(a *Account) {
		a.Tier = "free"
		a.ExpiresAt = time.Time{}
		if a.LapsedAt.IsZero() {
			a.LapsedAt = time.Now()
		}
	})
}

func (s *pgAccountStore) ExpiredPaid(now time.Time) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for _, a := range s.byKey {
		if a.Tier != "free" && !a.ExpiresAt.IsZero() && now.After(a.ExpiresAt) {
			out = append(out, a.Email)
		}
	}
	sort.Strings(out)
	return out
}

func (s *pgAccountStore) Snapshot(key string) (Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.byKey[key]
	if !ok {
		return Account{}, false
	}
	cp := *a
	if len(a.Domains) > 0 {
		cp.Domains = append([]string(nil), a.Domains...)
	}
	return cp, true
}

// Mutate applies fn under the lock and persists the result.
func (s *pgAccountStore) Mutate(email string, fn func(*Account)) bool {
	s.mu.Lock()
	a, ok := s.byMail[email]
	if !ok {
		s.mu.Unlock()
		return false
	}
	fn(a)
	s.mu.Unlock()
	_ = s.upsert(context.Background(), a)
	return true
}

// TryClaimDomain mirrors the JSON store's atomic claim (#101): uniqueness
// check, gate and append under one lock, then async-ish persist.
func (s *pgAccountStore) TryClaimDomain(email, dom string, mayClaim func(*Account) bool) bool {
	s.mu.Lock()
	a, ok := s.byMail[email]
	if !ok {
		s.mu.Unlock()
		return false
	}
	if containsStr(a.Domains, dom) {
		s.mu.Unlock()
		return true // idempotent re-add
	}
	for _, other := range s.byKey {
		if other.Email != email && containsStr(other.Domains, dom) {
			s.mu.Unlock()
			return false
		}
	}
	if mayClaim != nil && !mayClaim(a) {
		s.mu.Unlock()
		return false
	}
	a.Domains = append(a.Domains, dom)
	s.mu.Unlock()
	_ = s.upsert(context.Background(), a)
	return true
}

func (s *pgAccountStore) List() []*Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Account, 0, len(s.byKey))
	for _, a := range s.byKey {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// PruneInactive keeps accounts that ever paid or own domains.
func (s *pgAccountStore) PruneInactive(olderThan time.Duration, now time.Time) int {
	s.mu.RLock()
	var doomed []string
	for _, a := range s.byKey {
		if a.EverPaid() || len(a.Domains) > 0 {
			continue
		}
		if now.Sub(a.Created) > olderThan {
			doomed = append(doomed, a.APIKey)
		}
	}
	s.mu.RUnlock()
	for _, k := range doomed {
		s.Delete(k)
	}
	return len(doomed)
}

// Reload forces a cache refresh (used after a peer signals a change).
func (s *pgAccountStore) Reload() error {
	return s.loadAll(context.Background())
}

// Close stops the poller and the pool.
func (s *pgAccountStore) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	s.pool.Close()
	return nil
}

// Compile-time interface check against the methods the server actually calls.
var _ interface {
	Create(string, string) (*Account, error)
	Lookup(string) (*Account, bool)
	LookupEmail(string) (*Account, bool)
	FindByProvider(string, string) (*Account, bool)
	Delete(string) bool
	ResetKey(string) (string, bool)
	CancelSubscription(string) bool
	DowngradeToFree(string) bool
	ExpiredPaid(time.Time) []string
	Snapshot(string) (Account, bool)
	Mutate(string, func(*Account)) bool
	List() []*Account
	PruneInactive(time.Duration, time.Time) int
	Close() error
} = (*pgAccountStore)(nil)

var _ = errors.New // keep errors import if unused later
var _ = pgx.ErrNoRows
