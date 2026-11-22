package server

import (
	"errors"
	"sync"
)

// ErrSubdomainTaken is returned when a live tunnel already holds a subdomain.
var ErrSubdomainTaken = errors.New("etunnel: subdomain already in use")

// Registry tracks live tunnels by subdomain. Per-account live counts
// (total + custom) are maintained under the same lock so the bounded
// acquires are O(1) instead of scanning the whole table (#331 LOW5).
type Registry struct {
	mu         sync.RWMutex
	subs       map[string]*Session
	acctAll    map[string]int // api key -> live tunnels (all names)
	acctCustom map[string]int // api key -> live tunnels on custom names
}

func newRegistry() *Registry {
	return &Registry{
		subs:       make(map[string]*Session),
		acctAll:    make(map[string]int),
		acctCustom: make(map[string]int),
	}
}

// accountCounts returns the live (all, custom) counts for an account.
// Requires r.mu held. acct == nil means anonymous (unbounded).
func (r *Registry) accountCounts(acct *Account) (all, custom int) {
	if acct == nil {
		return 0, 0
	}
	return r.acctAll[acct.APIKey], r.acctCustom[acct.APIKey]
}

// accountRelease decrements the counters for a session being removed.
// Requires r.mu held. No-op for anonymous sessions.
func (r *Registry) accountRelease(acct *Account, custom bool) {
	if acct == nil {
		return
	}
	if r.acctAll[acct.APIKey] > 0 {
		r.acctAll[acct.APIKey]--
	}
	if custom && r.acctCustom[acct.APIKey] > 0 {
		r.acctCustom[acct.APIKey]--
	}
}

// Acquire assigns sub to s. If a dead session still holds it, it is replaced.
func (r *Registry) Acquire(sub string, s *Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.subs[sub]; ok && cur != s {
		select {
		case <-cur.closed:
			// dead session, replace it below
		default:
			return ErrSubdomainTaken
		}
	}
	r.subs[sub] = s
	return nil
}

// ErrAccountQuota is returned when the account's live custom-subdomain
// count is already at its plan limit (#304 A-3).
var ErrAccountQuota = errors.New("etunnel: account custom-subdomain quota exhausted")

// AcquireCustom assigns a custom subdomain to s, enforcing the account's
// custom-subdomain quota inside the same lock (custom-only; MaxTunnels is
// not consulted). Production paths use AcquireBounded; this thin wrapper
// survives for tests that exercise the custom quota in isolation.
func (r *Registry) AcquireCustom(sub string, s *Session, acct *Account, limit int) error {
	return r.AcquireBounded(sub, s, acct, 0, true, limit)
}

// AcquireBounded atomically counts the account's live tunnels (all names,
// plus the custom subset when checkCustom) and acquires sub in one critical
// section (#321 M1: an RLock pre-check plus a separate Acquire let two
// concurrent handshakes for one account each pass and exceed
// tier.MaxTunnels). acct == nil (anonymous) skips the account bounds.
// maxTunnels <= 0 means unlimited.
func (r *Registry) AcquireBounded(sub string, s *Session, acct *Account, maxTunnels int, checkCustom bool, maxCustom int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.subs[sub]; ok && cur != s {
		select {
		case <-cur.closed:
			// #336 HIGH: dead session being replaced - hand its slot back
			// to the account counters NOW. The old holder's own Release is
			// swallowed by the cur==s guard once subs[sub] points at the
			// replacement, so skipping this decrements leaked a count per
			// replacement and locked the account out of its quota forever.
			r.accountRelease(cur.account, cur.custom)
		default:
			return ErrSubdomainTaken
		}
	}
	if acct == nil {
		r.subs[sub] = s
		return nil
	}
	// #331 LOW5: O(1) counters instead of scanning r.subs.
	all, custom := r.accountCounts(acct)
	if maxTunnels > 0 && all >= maxTunnels {
		return ErrAccountQuota
	}
	if checkCustom && custom >= maxCustom {
		return ErrAccountQuota
	}
	// #321 M1: mark custom inside the lock - the counters above read this
	// flag, and setting it only after release re-opened the exact TOCTOU
	// this method exists to close.
	if checkCustom {
		s.custom = true
	}
	r.subs[sub] = s
	if acct != nil {
		r.acctAll[acct.APIKey]++
		if checkCustom {
			r.acctCustom[acct.APIKey]++
		}
	}
	return nil
}

// Release frees sub if it is currently held by s.
func (r *Registry) Release(sub string, s *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.subs[sub]; ok && cur == s {
		delete(r.subs, sub)
		r.accountRelease(s.account, s.custom) // #331 LOW5: keep O(1) counts true
	}
}

// Lookup returns the live session for sub, or nil.
func (r *Registry) Lookup(sub string) *Session {
	r.mu.RLock()
	s := r.subs[sub]
	r.mu.RUnlock()
	if s == nil {
		return nil
	}
	select {
	case <-s.closed:
		return nil
	default:
		return s
	}
}

// Count returns the number of live tunnels.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, s := range r.subs {
		select {
		case <-s.closed:
		default:
			n++
		}
	}
	return n
}

// Snapshot returns all live sessions.
func (r *Registry) Snapshot() []*Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*Session
	for _, s := range r.subs {
		select {
		case <-s.closed:
		default:
			out = append(out, s)
		}
	}
	return out
}

// countForAccount counts live tunnels (any subdomain) belonging to the account
// with the given API key.
func (r *Registry) countForAccount(apiKey string) int {
	if apiKey == "" {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, s := range r.subs {
		if s.account == nil || s.account.APIKey != apiKey {
			continue
		}
		select {
		case <-s.closed:
		default:
			n++
		}
	}
	return n
}

// countCustomForAccount counts live tunnels on custom (non-random) subdomains
// belonging to the given account.
func (r *Registry) countCustomForAccount(acct *Account) int {
	if acct == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, s := range r.subs {
		if s.account != nil && s.account.APIKey == acct.APIKey && s.custom {
			select {
			case <-s.closed:
			default:
				n++
			}
		}
	}
	return n
}
