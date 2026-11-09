package server

// In-memory Counter: the single-node default. Semantically identical to the
// pre-cluster checks (count live sessions per IP/account in the Registry) but
// expressed through the Counter interface so the Redis implementation can
// replace it with no call-site changes.

import (
	"context"
	"sync"
	"time"
)

type memCounter struct {
	mu   sync.Mutex
	vals map[string]int64
	exp  map[string]time.Time // key -> when it expires (zero = never)
}

func newMemCounter() *memCounter {
	return &memCounter{vals: map[string]int64{}, exp: map[string]time.Time{}}
}

func (m *memCounter) Incr(_ context.Context, key string, delta int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vals[key] += delta
	return m.vals[key], nil
}

func (m *memCounter) Decr(ctx context.Context, key string) error {
	_, err := m.Incr(ctx, key, -1)
	return err
}

func (m *memCounter) Get(_ context.Context, key string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if exp, ok := m.exp[key]; ok && !exp.IsZero() && time.Now().After(exp) {
		return 0, nil
	}
	return m.vals[key], nil
}

func (m *memCounter) SetTTL(_ context.Context, key string, d time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d <= 0 {
		delete(m.exp, key) // never expires
	} else {
		m.exp[key] = time.Now().Add(d)
	}
	return nil
}
