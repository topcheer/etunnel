package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ErrUnknownTier indicates registration with an undefined tier.
var ErrUnknownTier = errors.New("etunnel: unknown tier")

// ErrEmailTaken indicates an account already exists for the email.
var ErrEmailTaken = errors.New("etunnel: email already registered")

// accountBackend is the account storage contract shared by the JSON store
// (single node) and the Postgres store (cluster).
type accountBackend interface {
	Create(email, tier string) (*Account, error)
	Lookup(key string) (*Account, bool)
	TryClaimDomain(email, dom string, mayClaim func(*Account) bool) bool
	LookupEmail(email string) (*Account, bool)
	FindByProvider(provider, subject string) (*Account, bool)
	Delete(key string) bool
	// DeleteWithError deletes and reports persistence failure (#366: a
	// failed delete resurrected accounts on restart while callers 200'd).
	DeleteWithError(key string) error
	ResetKey(email string) (string, bool)
	CancelSubscription(email string) bool
	DowngradeToFree(email string) bool
	ExpiredPaid(now time.Time) []string
	Snapshot(key string) (Account, bool)
	Mutate(email string, fn func(*Account)) bool
	// mutate also reports a persistence failure, for callers that must not
	// answer "ok" when the write did not reach storage (#218).
	mutate(email string, fn func(*Account)) (bool, error)
	List() []Account
	PruneInactive(olderThan time.Duration, now time.Time) int
}

// ShortDomain is the purchased entitlement to register premium short
// subdomains (1-3 characters). Len is the maximum name length covered.
type ShortDomain struct {
	Len       int       `json:"len"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Account is a registered (paid-plan) identity identified by API key.
type Account struct {
	APIKey  string    `json:"apiKey"`
	Email   string    `json:"email"`
	Tier    string    `json:"tier"`
	Created time.Time `json:"created"`
	// OAuth identity (optional; anonymous email-registered accounts have none).
	Provider string `json:"provider,omitempty"` // google | gitlab | apple
	Subject  string `json:"subject,omitempty"`  // provider-stable user id
	Name     string `json:"name,omitempty"`     // display name
	// Custom subdomains the account owns (paid tiers, count-limited by tier).
	Domains []string `json:"domains,omitempty"`
	// ExpiresAt is when the paid subscription lapses; zero means no expiry
	// (free tier, or an account granted a tier manually). The reaper in
	// Server.startTierReaper downgrades expired accounts to free.
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
	// SessionEpoch invalidates browser sessions when bumped (logout,
	// API-key reset): the session-cookie MAC folds it in (#295). Accounts
	// created before the field existed carry 0, so the rollout does not
	// force a global re-login.
	SessionEpoch int64 `json:"sessionEpoch,omitempty"`
	// PaidShort is the purchased entitlement to register 1-3 character
	// subdomains (one-time purchase per subscription period; Free accounts
	// can hold one without a paid tier).
	PaidShort *ShortDomain `json:"paidShort,omitempty"`
	// LapsedAt records when a paid subscription expired (zero = never paid).
	// It survives the downgrade to free so retention logic can keep the
	// account: a lapsed customer must never be garbage-collected like an
	// anonymous free account - their payment history and OAuth binding are
	// worth keeping for support, refunds and re-subscription.
	LapsedAt time.Time `json:"lapsedAt,omitempty"`
	// Passkeys are the account's WebAuthn credentials (server/webauthn.go);
	// empty means none registered.
	Passkeys []Passkey `json:"passkeys,omitempty"`
}

// AccountStore persists accounts in a JSON file.
type AccountStore struct {
	mu     sync.RWMutex
	path   string
	byKey  map[string]*Account
	byMail map[string]*Account
	// onSaveErr, when set, receives every persistence failure so the
	// operator sees them (#93: these used to be silently discarded, which
	// split in-memory state from disk across restarts).
	onSaveErr func(error)
	// recoveryNote, when set, describes a startup recovery event (#18):
	// the accounts file was corrupt and got preserved aside; the server
	// logs it loudly once it has a logger.
	recoveryNote string
}

// NewAccountStore creates a store, loading persisted accounts when path is set.
// A corrupt (torn/hand-edited) file no longer bricks the daemon (#18): the
// file is preserved as accounts.json.corrupt-<ts>, the store starts empty,
// and the event is surfaced via RecoveryNote for the server to log loudly.
func NewAccountStore(path string) (*AccountStore, error) {
	s := &AccountStore{
		path:   path,
		byKey:  make(map[string]*Account),
		byMail: make(map[string]*Account),
	}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, err
	}
	if len(b) == 0 {
		return s, nil
	}
	var accounts []*Account
	if err := json.Unmarshal(b, &accounts); err != nil {
		backup := fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix())
		if renameErr := os.Rename(path, backup); renameErr != nil {
			return nil, fmt.Errorf("etunnel: parse accounts file: %w (and preserving it as %s failed: %v)", err, backup, renameErr)
		}
		s.recoveryNote = fmt.Sprintf("accounts file was corrupt (%v); preserved as %s and started EMPTY - accounts must be re-registered or restored from the backup", err, backup)
		return s, nil
	}
	for _, a := range accounts {
		s.byKey[a.APIKey] = a
		s.byMail[a.Email] = a
	}
	return s, nil
}

func (s *AccountStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	accounts := make([]*Account, 0, len(s.byKey))
	for _, a := range s.byKey {
		accounts = append(accounts, a)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Created.Before(accounts[j].Created) })
	b, err := json.MarshalIndent(accounts, "", "  ")
	if err != nil {
		s.reportSaveErr(err)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		s.reportSaveErr(err)
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		s.reportSaveErr(err)
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		s.reportSaveErr(err)
		return err
	}
	return nil
}

// reportSaveErr surfaces a persistence failure through the injected
// logger (no-op when unset, e.g. tests).
func (s *AccountStore) reportSaveErr(err error) {
	if s.onSaveErr != nil {
		s.onSaveErr(err)
	}
}

func newAPIKey() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "et_" + hex.EncodeToString(b)
}

// Create registers a new account and returns it with a fresh API key.
func (s *AccountStore) Create(email, tier string) (*Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byMail[email]; ok {
		return nil, ErrEmailTaken
	}
	a := &Account{
		APIKey:  newAPIKey(),
		Email:   email,
		Tier:    tier,
		Created: time.Now().UTC(),
	}
	s.byKey[a.APIKey] = a
	s.byMail[a.Email] = a
	return a, s.saveLocked()
}

// Lookup resolves an API key to an account.
func (s *AccountStore) Lookup(key string) (*Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.byKey[key]
	return a, ok
}

// LookupEmail resolves an account by email.
func (s *AccountStore) LookupEmail(email string) (*Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.byMail[email]
	return a, ok
}

// Snapshot resolves an API key to a deep copy of the account.
//
// Lookup returns the store's live *Account; callers that read its fields
// outside s.mu (session handshake, stats) would race Mutate/SaveLocked
// writers. Snapshot hands back a value the caller owns, so concurrent tier or
// domain changes cannot tear a read (issue #4).
func (s *AccountStore) Snapshot(key string) (Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.byKey[key]
	if !ok {
		return Account{}, false
	}
	cp := *a
	if len(a.Domains) > 0 { // copy the slice: a shared header can tear
		cp.Domains = append([]string(nil), a.Domains...)
	}
	return cp, true
}

// Delete removes an account by API key.
func (s *AccountStore) Delete(key string) bool {
	return s.DeleteWithError(key) == nil
}

// DeleteWithError deletes the account and surfaces persistence failure
// (#366): memory is mutated first (the operator can reconcile), but the
// error tells the caller the account will reappear on restart.
func (s *AccountStore) DeleteWithError(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byKey[key]
	if !ok {
		return errAccountNotFound
	}
	delete(s.byKey, key)
	delete(s.byMail, a.Email)
	if err := s.saveLocked(); err != nil {
		// memory is already mutated; surface so operators can reconcile
		return fmt.Errorf("persist delete for %s: %w", a.Email, err)
	}
	return nil
}

// errAccountNotFound is returned by DeleteWithError for unknown keys.
var errAccountNotFound = errors.New("etunnel: account not found")

// ResetKey issues a fresh API key for the account (identity/tier preserved).
func (s *AccountStore) ResetKey(email string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byMail[email]
	if !ok {
		return "", false
	}
	delete(s.byKey, a.APIKey)
	a.APIKey = newAPIKey()
	s.byKey[a.APIKey] = a
	if err := s.saveLocked(); err != nil {
		// surfaced via reportSaveErr; the new key stays valid in-memory
	}
	return a.APIKey, true
}

// CancelSubscription downgrades the account to the free tier.
func (s *AccountStore) CancelSubscription(email string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byMail[email]
	if !ok {
		return false
	}
	a.Tier = "free"
	if err := s.saveLocked(); err != nil {
		// surfaced via reportSaveErr
	}
	return true
}

// TryClaimDomain atomically adds dom to the account ONLY if no other
// account holds it (#101: the old lock-free subdomainTakenBy precheck
// raced a concurrent Mutate, allowing the same name on two accounts).
// The uniqueness check, the tier/entitlement gate and the append all run
// under one critical section; mayClaim runs inside the lock.
func (s *AccountStore) TryClaimDomain(email, dom string, mayClaim func(*Account) bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byMail[email]
	if !ok {
		return false
	}
	if containsStr(a.Domains, dom) {
		return true // idempotent re-add on the same account
	}
	for _, other := range s.byKey {
		if other.Email != email && containsStr(other.Domains, dom) {
			return false
		}
	}
	if mayClaim != nil && !mayClaim(a) {
		return false
	}
	a.Domains = append(a.Domains, dom)
	if err := s.saveLocked(); err != nil {
		// surfaced via reportSaveErr (#93); memory keeps the claim so the
		// operator reconciles from the log rather than silently losing it
	}
	return true
}

// FindByProvider resolves an account by OAuth provider + subject.
func (s *AccountStore) FindByProvider(provider, subject string) (*Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.byKey {
		if a.Provider == provider && a.Subject == subject {
			return a, true
		}
	}
	return nil, false
}

// Mutate applies fn to the account under the store lock and persists.
// It reports only whether the account existed; use mutate when a failed write
// must not be reported as success (payment activation, #218).
func (s *AccountStore) Mutate(email string, fn func(*Account)) bool {
	ok, _ := s.mutate(email, fn)
	return ok
}

// mutate applies fn under the lock and returns both facts callers need:
// whether the account existed, and whether the change reached storage.
//
// The distinction matters where money is involved: a tier upgrade that only
// lives in memory is a customer charged for nothing after the next restart, so
// the caller must be able to fail instead of answering 200 (#218).
func (s *AccountStore) mutate(email string, fn func(*Account)) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byMail[email]
	if !ok {
		return false, nil
	}
	fn(a)
	if err := s.saveLocked(); err != nil {
		return true, err // found, mutated, but not durable
	}
	return true, nil
}

// List returns all accounts sorted by creation time.
// List returns a value snapshot of every account (#356: the old []*Account
// handed out live pointers that readers raced against billing/OAuth writes).
func (s *AccountStore) List() []Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Account, 0, len(s.byKey))
	for _, a := range s.byKey {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// ExpiredPaid returns emails of paid accounts whose subscription lapsed.
func (s *AccountStore) ExpiredPaid(now time.Time) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for _, a := range s.byKey {
		if a.Tier != "free" && !a.ExpiresAt.IsZero() && now.After(a.ExpiresAt) {
			out = append(out, a.Email)
		}
	}
	return out
}

// DowngradeToFree sets the tier to free and clears the expiry.
func (s *AccountStore) DowngradeToFree(email string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byMail[email]
	if !ok {
		return false
	}
	a.Tier = "free"
	a.ExpiresAt = time.Time{}
	if a.LapsedAt.IsZero() {
		a.LapsedAt = time.Now() // keep the first lapse; do not overwrite
	}
	// #265: a lost downgrade write resurrects the paid tier (quota and
	// entitlements) on the next restart - surface the failure through the
	// store's standard onSaveErr channel instead of discarding it.
	if err := s.saveLocked(); err != nil {
		s.reportSaveErr(err)
	}
	return true
}

// EverPaid reports whether the account has ever had a paid subscription
// (currently paid or lapsed). Retention/GC logic must never delete such an
// account: it carries payment history and an OAuth binding.
func (a *Account) EverPaid() bool {
	return a.Tier != "free" || !a.LapsedAt.IsZero()
}

// PruneInactive removes anonymous free accounts older than the retention
// window. Accounts that ever paid (EverPaid) or own custom subdomains are
// always kept - their history and OAuth bindings outlive the retention
// policy. No-op when retention is zero.
func (s *AccountStore) PruneInactive(olderThan time.Duration, now time.Time) int {
	if olderThan <= 0 {
		return 0
	}
	s.mu.Lock()
	var doomed []string
	for _, a := range s.byKey {
		if a.EverPaid() || len(a.Domains) > 0 {
			continue
		}
		if now.Sub(a.Created) > olderThan {
			doomed = append(doomed, a.APIKey)
		}
	}
	s.mu.Unlock()
	n := 0
	for _, k := range doomed {
		if s.Delete(k) {
			n++
		}
	}
	return n
}
