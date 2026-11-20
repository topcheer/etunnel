package server

// Per-IP anonymous tunnel limit (handshake side). Extracted from the
// websocket handshake (#232) so the failure modes are unit-testable.

import (
	"context"
	"time"
)

// takeIPSlot enforces the anonymous per-IP tunnel limit for one handshake.
// It returns the counter that granted the slot; nil means the handshake must
// be rejected with ErrCodeTooManyTunnels.
//
// #232: a shared-counter failure no longer fails open. The decision degrades
// to the process-local counter - the limit stays enforced per-node instead
// of vanishing exactly when the backend flaps. Any phantom INCR the backend
// may have applied before failing is compensated in the background: the
// decrement script deletes the key at <= 0 (#36), so a blind Decr is safe
// whether or not the INCR landed. Without it, a no-TTL phantom slot could
// lock a single-tunnel IP out forever (every later handshake hit the
// over-limit branch, which never sets the TTL).
func (s *Server) takeIPSlot(ctx context.Context, ip string) Counter {
	key := "ip:" + ip
	n, err := s.counter.Incr(ctx, key, 1)
	if err == nil {
		if n > int64(s.cfg.MaxTunnelsPerIP) {
			_ = s.counter.Decr(ctx, key)
			return nil
		}
		_ = s.counter.SetTTL(ctx, key, ipCounterTTL)
		return s.counter
	}
	s.logf("ip %s: shared counter unavailable, degrading per-IP limit to local: %v", ip, err)
	go func() {
		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.counter.Decr(cctx, key)
	}()
	local := s.localCounter()
	m, merr := local.Incr(ctx, key, 1)
	if merr != nil {
		// No protection is possible at all: fail closed.
		return nil
	}
	if m > int64(s.cfg.MaxTunnelsPerIP) {
		_ = local.Decr(ctx, key)
		return nil
	}
	_ = local.SetTTL(ctx, key, ipCounterTTL)
	return local
}

// localCounter lazily builds the process-local fallback counter used when
// the shared counter is unavailable (#232).
func (s *Server) localCounter() Counter {
	s.memOnce.Do(func() { s.mem = newMemCounter() })
	return s.mem
}
