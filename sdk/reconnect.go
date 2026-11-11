package etunnel

// Reconnecting tunnels: a supervisor that rebuilds the underlying tunnel when
// the connection drops. The motivating case is a server restart - the tunnel
// dies, the supervisor dials again with exponential backoff, and the public
// URL comes back without the operator touching anything.
//
// The single-connection Tunnel in etunnel.go is deliberately left untouched:
// this wrapper owns the retry policy, so a bug here cannot corrupt the data
// path. Callers that need a stable handle across reconnects use
// ReconnectingTunnel; each successful reconnect may change URL() (anonymous
// tunnels get a fresh random subdomain unless a paid custom domain is set).

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/topcheer/etunnel/protocol"
)

// Reconnect defaults.
const (
	DefaultReconnectMinBackoff = 500 * time.Millisecond
	DefaultReconnectMaxBackoff = 30 * time.Second
	reconnectBackoffFactor     = 2.0
)

// ReconnectingTunnel keeps a tunnel alive across disconnects. It is safe for
// concurrent use; every accessor proxies to the current underlying tunnel.
type ReconnectingTunnel struct {
	opts Options

	mu      sync.Mutex
	cur     *Tunnel
	url     string // last known URL, kept for reads during a reconnect window
	sub     string
	attempt int // reconnects performed so far

	ctx    context.Context
	cancel context.CancelFunc

	closed    chan struct{}
	closeOnce sync.Once
	userClose atomic.Bool // Close() was called by the user: no reconnects
}

// ConnectReconnecting dials once (returning the first error, so bad
// credentials fail fast rather than looping forever) and then supervises the
// connection in the background.
func ConnectReconnecting(ctx context.Context, opts Options) (*ReconnectingTunnel, error) {
	if opts.LocalAddr == "" {
		return nil, errors.New("etunnel: LocalAddr is required")
	}
	rctx, cancel := context.WithCancel(ctx)
	r := &ReconnectingTunnel{
		opts:   opts,
		ctx:    rctx,
		cancel: cancel,
		closed: make(chan struct{}),
	}
	first, err := Connect(rctx, opts)
	if err != nil {
		cancel()
		return nil, err
	}
	r.cur = first
	r.url, r.sub = first.URL(), first.Subdomain()
	go r.supervise()
	return r, nil
}

// supervise rebuilds the tunnel whenever the current one ends without the
// user asking for it.
func (r *ReconnectingTunnel) supervise() {
	defer close(r.closed)
	// Release the supervisor context on every exit path (permanent error,
	// parent cancel, user close) - without this the WithCancel registration
	// leaks until the parent context ends (issue #55).
	defer r.cancel()
	backoff := r.opts.ReconnectMinBackoff
	if backoff <= 0 {
		backoff = DefaultReconnectMinBackoff
	}
	maxBackoff := r.opts.ReconnectMaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = DefaultReconnectMaxBackoff
	}
	for {
		r.mu.Lock()
		cur := r.cur
		r.mu.Unlock()
		if cur == nil {
			return
		}
		select {
		case <-cur.Done():
		case <-r.ctx.Done():
			return
		}
		if r.userClose.Load() {
			return
		}
		// The drop was not requested by us: reconnect.
		for {
			if r.ctx.Err() != nil || r.userClose.Load() {
				return
			}
			r.logf("connection lost (%s); reconnecting in %s", cur.CloseReason(), backoff)
			select {
			case <-time.After(jitter(backoff)):
			case <-r.ctx.Done():
				return
			}
			// Replay the last resume token so the client reclaims its
			// subdomain instead of getting a fresh random one.
			attemptOpts := r.opts
			r.mu.Lock()
			if curTun := r.cur; curTun != nil {
				attemptOpts.resume = curTun.ResumeToken()
			}
			r.mu.Unlock()
			next, err := Connect(r.ctx, attemptOpts)
			if err != nil {
				if isPermanent(err) {
					r.logf("reconnect refused permanently: %v", err)
					return
				}
				r.logf("reconnect failed: %v", err)
				backoff = grow(backoff, maxBackoff)
				continue
			}
			r.mu.Lock()
			r.cur, r.url, r.sub = next, next.URL(), next.Subdomain()
			r.attempt++
			attempt := r.attempt
			url := next.URL()
			r.mu.Unlock()
			if r.opts.OnReconnect != nil {
				r.opts.OnReconnect(attempt, url)
			}
			r.logf("reconnected after %d attempt(s): %s", attempt, url)
			backoff = r.opts.ReconnectMinBackoff // reset after success
			if backoff <= 0 {
				backoff = DefaultReconnectMinBackoff
			}
			break
		}
	}
}

// isPermanent marks errors that retrying cannot fix (bad token, bad plan).
// Codes come from protocol.ErrCode* so the compiler catches drift; note the
// server's invalid-name code is "subdomain_invalid" (the previous literal
// "subdomain_bad" never matched what the server sends).
func isPermanent(err error) bool {
	// a strictly-newer server protocol is unfixable by retrying (#14 F#5)
	if errors.Is(err, ErrUnsupportedProto) {
		return true
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case protocol.ErrCodeUnauthorized,
			protocol.ErrCodeSubdomainPlan,
			protocol.ErrCodeSubdomainBad,
			protocol.ErrCodeBadMessage:
			return true
		}
	}
	return false
}

func grow(backoff, max time.Duration) time.Duration {
	next := time.Duration(float64(backoff) * reconnectBackoffFactor)
	if next > max {
		next = max
	}
	if next <= 0 {
		next = max
	}
	return next
}

// jitter spreads reconnect storms: +-10% (issue #55: comment previously
// claimed 20%; the implementation below has always delivered 10%).
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	delta := time.Duration(rand.Int63n(int64(d)/5 + 1))
	return d - d/10 + delta
}

func (r *ReconnectingTunnel) logf(format string, args ...any) {
	if r.opts.OnLog != nil {
		r.opts.OnLog(format, args...)
	}
}

// URL returns the live public URL (last known while reconnecting).
func (r *ReconnectingTunnel) URL() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.url
}

// Subdomain returns the current subdomain label.
func (r *ReconnectingTunnel) Subdomain() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sub
}

// Attempts reports how many successful reconnects have happened.
func (r *ReconnectingTunnel) Attempts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempt
}

// Tunnel returns the current underlying tunnel, or nil while a reconnect
// is in flight. Accessors like ResumeToken/CloseReason live on it.
func (r *ReconnectingTunnel) Tunnel() *Tunnel {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cur
}

// Done is closed when the supervisor stops: the user called Close, the parent
// context ended, or reconnecting failed permanently.
func (r *ReconnectingTunnel) Done() <-chan struct{} { return r.closed }

// Close stops the supervisor and the current tunnel. No reconnect follows.
func (r *ReconnectingTunnel) Close(reason string) {
	r.closeOnce.Do(func() {
		r.userClose.Store(true)
		if reason == "" {
			reason = "client closed"
		}
		r.mu.Lock()
		cur := r.cur
		r.mu.Unlock()
		if cur != nil {
			cur.Close(reason)
		}
		r.cancel()
	})
}

// String renders the tunnel for logs.
func (r *ReconnectingTunnel) String() string {
	return fmt.Sprintf("etunnel[%s]", r.URL())
}
