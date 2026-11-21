package server

import (
	"errors"
	"sync"
)

// ErrSubdomainTaken is returned when a live tunnel already holds a subdomain.
var ErrSubdomainTaken = errors.New("etunnel: subdomain already in use")

// ErrCustomQuotaExhausted is returned by AcquireCustom when the account
// already holds the plan's full share of live custom subdomains (#304 A-3).
var ErrCustomQuotaExhausted = errors.New("etunnel: custom subdomain quota exhausted")

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

// AcquireCustom atomically recounts the account's live custom tunnels and
// acquires sub in one critical section (#304 A-3: a count under RLock and an
// acquire under Lock are two critical sections - concurrent handshakes for
// the same account can each pass the check and exceed the plan quota).
func (r *Registry) AcquireCustom(sub string, s *Session, acct *Account, maxCustom int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	used := 0
	for _, cur := range r.subs {
		if cur == s || cur.account == nil || cur.account.APIKey != acct.APIKey || !cur.custom {
			continue
		}
		select {
		case <-cur.closed:
		default:
			used++
		}
	}
	if used >= maxCustom {
		return ErrCustomQuotaExhausted
	}
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
