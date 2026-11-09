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

// Account is a registered (paid-plan) identity identified by API key.
type Account struct {
	APIKey  string    `json:"apiKey"`
	Email   string    `json:"email"`
	Tier    string    `json:"tier"`
	Created time.Time `json:"created"`
	// LastSeen is stamped by Snapshot (the handshake lookup) so stale accounts
	// can be pruned by activity, not by age - a long-lived paying account is
	// not stale just because it is old (issue #18).
	LastSeen time.Time `json:"lastSeen,omitempty"`
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
	// LapsedAt records when a paid subscription expired (zero = never paid).
	// It survives the downgrade to free so retention logic can keep the
	// account: a lapsed customer must never be garbage-collected like an
	// anonymous free account - their payment history and OAuth binding are
	// worth keeping for support, refunds and re-subscription.
	LapsedAt time.Time `json:"lapsedAt,omitempty"`
}

// AccountStore persists accounts in a JSON file.
type AccountStore struct {
	mu     sync.RWMutex
	path   string
	byKey  map[string]*Account
	byMail map[string]*Account
}

// NewAccountStore creates a store, loading persisted accounts when path is set.
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
		// A torn or hand-edited file must not brick the daemon (issue #18):
		// keep the bytes for recovery, start empty, and say so loudly.
		broken := fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix())
		if rerr := os.Rename(path, broken); rerr != nil {
			return nil, fmt.Errorf("etunnel: parse accounts file: %w (could not move it aside: %v)", err, rerr)
		}
		fmt.Fprintf(os.Stderr, "etunneld: accounts file %s was unparseable (%v); moved to %s, starting with an empty account store\n", path, err, broken)
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
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
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
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byKey[key]
	if !ok {
		return Account{}, false
	}
	a.LastSeen = time.Now() // activity stamp for pruning; persisted on next save
	cp := *a
	if len(a.Domains) > 0 { // copy the slice: a shared header can tear
		cp.Domains = append([]string(nil), a.Domains...)
	}
	return cp, true
}

// PruneInactive removes free-tier accounts with no custom domains whose last
// activity (LastSeen, falling back to Created) predates cutoff, and reports how
// many were removed. Paid accounts and accounts holding custom domains are
// never pruned: they carry entitlements. Live tunnels of a pruned account keep
// running until they close; only the API key stops working.
func (s *AccountStore) PruneInactive(cutoff time.Time, freeTier string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for key, a := range s.byKey {
		if a.Tier != freeTier || len(a.Domains) > 0 {
			continue
		}
		seen := a.LastSeen
		if seen.IsZero() {
			seen = a.Created
		}
		if seen.After(cutoff) {
			continue
		}
		delete(s.byKey, key)
		delete(s.byMail, a.Email)
		removed++
	}
	if removed > 0 {
		_ = s.saveLocked()
	}
	return removed
}

// Delete removes an account by API key.
func (s *AccountStore) Delete(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byKey[key]
	if !ok {
		return false
	}
	delete(s.byKey, key)
	delete(s.byMail, a.Email)
	_ = s.saveLocked()
	return true
}

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
	_ = s.saveLocked()
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
	_ = s.saveLocked()
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
func (s *AccountStore) Mutate(email string, fn func(*Account)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byMail[email]
	if !ok {
		return false
	}
	fn(a)
	_ = s.saveLocked()
	return true
}

// List returns all accounts sorted by creation time.
func (s *AccountStore) List() []*Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Account, 0, len(s.byKey))
	for _, a := range s.byKey {
		out = append(out, a)
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
	_ = s.saveLocked()
	return true
}

// EverPaid reports whether the account has ever had a paid subscription
// (currently paid or lapsed). Retention/GC logic must never delete such an
// account: it carries payment history and an OAuth binding.
func (a *Account) EverPaid() bool {
	return a.Tier != "free" || !a.LapsedAt.IsZero()
}
