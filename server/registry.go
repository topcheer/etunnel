package server

import (
	"errors"
	"sync"
)

// ErrSubdomainTaken is returned when a live tunnel already holds a subdomain.
var ErrSubdomainTaken = errors.New("etunnel: subdomain already in use")

// Registry tracks live tunnels by subdomain.
type Registry struct {
	mu   sync.RWMutex
	subs map[string]*Session
}

func newRegistry() *Registry {
	return &Registry{subs: make(map[string]*Session)}
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
// live-custom-tunnel quota inside the SAME lock that performs the acquire
// (#304 A-3: the previous count(RLock)-then-Acquire(Lock) split let two
// concurrent resumes of different custom subdomains for one account both
// pass the check).
func (r *Registry) AcquireCustom(sub string, s *Session, acct *Account, limit int) error {
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
	if limit > 0 && acct != nil {
		n := 0
		for _, live := range r.subs {
			if live == s || live.account == nil || live.account.APIKey != acct.APIKey || !live.custom {
				continue
			}
			select {
			case <-live.closed:
			default:
				n++
			}
		}
		if n >= limit {
			return ErrAccountQuota
		}
	}
	r.subs[sub] = s
	return nil
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
			// dead session, replace it below
		default:
			return ErrSubdomainTaken
		}
	}
	if acct == nil {
		r.subs[sub] = s
		return nil
	}
	all, custom := 0, 0
	for _, live := range r.subs {
		if live == s || live.account == nil || live.account.APIKey != acct.APIKey {
			continue
		}
		select {
		case <-live.closed:
			continue
		default:
			// live session: count it (non-blocking, like countForAccount)
		}
		all++
		if live.custom {
			custom++
		}
	}
	if maxTunnels > 0 && all >= maxTunnels {
		return ErrAccountQuota
	}
	if checkCustom && custom >= maxCustom {
		return ErrAccountQuota
	}
	// #321 M1: mark custom inside the lock - the counting loop above reads
	// this flag, and setting it only after release re-opened the exact
	// TOCTOU this method exists to close.
	if checkCustom {
		s.custom = true
	}
	r.subs[sub] = s
	return nil
}

// Release frees sub if it is currently held by s.
func (r *Registry) Release(sub string, s *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.subs[sub]; ok && cur == s {
		delete(r.subs, sub)
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
