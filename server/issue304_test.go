package server

// #304 second-review regressions: A-3 quota atomicity, A-4 TTL timer arming,
// B-1 sameOrigin port stripping, C-1 Domain hostname validation, D-1 loud
// truncation on mid-body write failure.

import (
	"context"
	"errors"
	"fmt"
	"github.com/topcheer/etunnel/protocol"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A-3: quota recount must be atomic with the acquire.
func TestAcquireCustomQuotaAtomic(t *testing.T) {
	r := newRegistry()
	acct := &Account{APIKey: "acct-1"}
	first := &Session{closed: make(chan struct{}), account: acct, custom: true}
	if err := r.AcquireCustom("svc-a", first, acct, 1); err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	second := &Session{closed: make(chan struct{}), account: acct, custom: true}
	if err := r.AcquireCustom("svc-b", second, acct, 1); !errors.Is(err, ErrAccountQuota) {
		t.Fatalf("expected quota error, got %v", err)
	}
	// 并发双 resume：8 goroutines 抢不同 custom 子域，limit=1 → 零额外成功
	r2 := newRegistry()
	anchor := &Session{closed: make(chan struct{}), account: acct, custom: true}
	if err := r2.AcquireCustom("anchor", anchor, acct, 1); err != nil {
		t.Fatalf("anchor acquire failed: %v", err)
	}
	var extra int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := &Session{closed: make(chan struct{}), account: acct, custom: true}
			if err := r2.AcquireCustom(fmt.Sprintf("sub-%d", i), s, acct, 1); err == nil {
				atomic.AddInt32(&extra, 1)
			}
		}(i)
	}
	wg.Wait()
	if atomic.LoadInt32(&extra) != 0 {
		t.Fatalf("quota raced: %d extra acquires slipped past the limit", extra)
	}
	// 另一账号不受影响
	other := &Account{APIKey: "acct-2"}
	sOther := &Session{closed: make(chan struct{}), account: other, custom: true}
	if err := r2.AcquireCustom("other", sOther, other, 1); err != nil {
		t.Fatalf("other account blocked by first account's quota: %v", err)
	}
}

// A-4: the timer gate is the expires value, not the tier TTL.
func TestTTLTimerGateOnExpiresValue(t *testing.T) {
	s := &Server{}
	armed := &Session{expires: time.Now().Add(time.Hour)}
	s.armTTLTimer(armed)
	if armed.ttlTimer == nil {
		t.Fatalf("timer not armed for non-zero expires (TTL-0 upgrade resume would outlive its expiry)")
	}
	armed.ttlTimer.Stop()

	zero := &Session{}
	s.armTTLTimer(zero)
	if zero.ttlTimer != nil {
		t.Fatalf("timer armed for zero expires")
	}
}

// B-1: Origin/Referer host comparison ignores the port.
func TestSameOriginStripsPort(t *testing.T) {
	s := &Server{cfg: Config{Domain: "gate.example.com"}}
	mk := func(header, v string) *http.Request {
		r := httptest.NewRequest("POST", "https://gate.example.com/auth/cli/grant", nil)
		r.Header.Set(header, v)
		return r
	}
	if !s.sameOrigin(mk("Origin", "https://gate.example.com:8443")) {
		t.Fatalf("non-default-port same-host Origin rejected")
	}
	if !s.sameOrigin(mk("Referer", "https://gate.example.com:8443/me")) {
		t.Fatalf("non-default-port same-host Referer rejected")
	}
	if !s.sameOrigin(mk("Origin", "https://GATE.EXAMPLE.com")) {
		t.Fatalf("case-insensitive same-host Origin rejected")
	}
	if s.sameOrigin(mk("Origin", "https://evil.example.com:8443")) {
		t.Fatalf("cross-origin accepted")
	}
	// 带端口的 cfg.Domain 也能匹配自身
	sPorted := &Server{cfg: Config{Domain: "gate.example.com:8443"}}
	if !sPorted.sameOrigin(mk("Origin", "https://gate.example.com:8443")) {
		t.Fatalf("ported cfg.Domain failed to match its own host")
	}
}

// C-1: Domain hostname validation refuses to start on illegal values.
func TestConfigNormalizeRejectsBadDomain(t *testing.T) {
	ok := &Config{Domain: "gate.example.com"}
	if err := ok.normalize(); err != nil {
		t.Fatalf("valid hostname rejected: %v", err)
	}
	ported := &Config{Domain: "gate.example.com:8443"}
	if err := ported.normalize(); err != nil {
		t.Fatalf("hostname with port rejected: %v", err)
	}
	xss := &Config{Domain: "</script>alert(1)</script>"}
	if err := xss.normalize(); err == nil {
		t.Fatalf("script-breaking Domain accepted")
	}
	space := &Config{Domain: "gate example com"}
	if err := space.normalize(); err == nil {
		t.Fatalf("space-bearing Domain accepted")
	}
}

// D-1: a mid-body write failure must abort loudly (h2 would otherwise end a
// no-Content-Length stream as silently-valid truncation), and a wrapped
// deadline error chain from the body pipe takes the same loud path.
func TestStreamTunnelBodyWriteErrorAbortsLoudly(t *testing.T) {
	s := newHOLSession(t)
	st := newHOLStream(t, s, 21)
	fw := &failingWriter{}
	req := httptest.NewRequest("GET", "https://dev.t.test/index.html", nil)
	go func() {
		_, _ = st.bodyPw.Write([]byte("partial payload"))
		_ = st.bodyPw.CloseWithError(io.ErrUnexpectedEOF)
	}()
	srv := &Server{metrics: &metrics{}}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected http.ErrAbortHandler panic on write failure, got clean return (silent truncation)")
		}
		if r != http.ErrAbortHandler {
			t.Fatalf("unexpected panic value: %v", r)
		}
	}()
	srv.streamTunnelBody(fw, req, s, st, &protocol.Res{Type: protocol.MsgRes, StreamID: 21, Status: 200})
}

func TestStreamTunnelBodyDeadlineChainAbortsLoudly(t *testing.T) {
	s := newHOLSession(t)
	st := newHOLStream(t, s, 22)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "https://dev.t.test/index.html", nil)
	go func() {
		_, _ = st.bodyPw.Write([]byte("first chunk"))
		_ = st.bodyPw.CloseWithError(fmt.Errorf("read timeout: %w", context.DeadlineExceeded))
	}()
	srv := &Server{metrics: &metrics{}}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected http.ErrAbortHandler for wrapped deadline error chain")
		}
	}()
	srv.streamTunnelBody(rec, req, s, st, &protocol.Res{Type: protocol.MsgRes, StreamID: 22, Status: 200})
}

type failingWriter struct{ header http.Header }

func (w *failingWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *failingWriter) Write(p []byte) (int, error) {
	return 0, errors.New("h2 stream reset mid-body")
}

func (w *failingWriter) WriteHeader(int) {}

var _ = strings.TrimSpace // keep import if unused by future edits
