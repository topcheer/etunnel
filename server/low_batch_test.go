package server

// #304 LOW batch regressions: A-3 quota/acquire atomicity, B-1 portless
// same-origin comparison, C-1 config charset validation, C-2 honest copy
// button, D-1 post-deadline write errors abort on h2 chains too.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/topcheer/etunnel/protocol"
)

// A-3: count and acquire share the registry lock. Eight concurrent
// handshakes for one account with quota 1 must yield exactly one winner.
func TestAcquireCustomQuotaAtomic(t *testing.T) {
	r := newRegistry()
	acct := &Account{APIKey: "k1"}
	const n = 8
	var wg sync.WaitGroup
	results := make(chan error, n)
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			ss := &Session{
				id:      fmt.Sprintf("id%d", i),
				sub:     fmt.Sprintf("s%d", i),
				account: acct,
				custom:  true,
				closed:  make(chan struct{}),
			}
			results <- r.AcquireCustom(fmt.Sprintf("s%d", i), ss, acct, 1)
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrCustomQuotaExhausted) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("quota 1 admitted %d concurrent tunnels, want exactly 1 (#304 A-3)", winners)
	}
}

// B-1: Origin/Referer hosts compare without ports.
func TestSameOriginIgnoresPort(t *testing.T) {
	s := newBillingTestServer(t)
	r := httptest.NewRequest(http.MethodPost, "https://t.test/x", nil)
	r.Header.Set("Origin", "https://t.test:8443") // portal on a non-default port
	if !s.sameOrigin(r) {
		t.Fatal("same-host origin with a port must match (#304 B-1)")
	}
	r.Header.Set("Referer", "https://t.test:8443/me")
	if !s.sameOrigin(r) {
		t.Fatal("same-host referer with a port must match (#304 B-1)")
	}
	r.Header.Set("Origin", "https://attacker.t.test:9")
	if s.sameOrigin(r) {
		t.Fatal("tunnel-subdomain origin must still be rejected")
	}
	r.Header.Set("Origin", "https://evil.test:8443")
	if s.sameOrigin(r) {
		t.Fatal("foreign host origin must be rejected")
	}
}

// C-1: operator-supplied Domain/WSPath are charset-checked at startup.
func TestConfigRejectsIllegalDomainAndWSPath(t *testing.T) {
	base := func(mut func(*Config)) *Config {
		c := Config{
			Domain:       "t.test",
			Addr:         "127.0.0.1:1",
			TLS:          TLSConfig{Mode: TLSModeHTTP},
			AccountsFile: filepath.Join(t.TempDir(), "a.json"),
			Tiers:        DefaultTiers(),
		}
		if mut != nil {
			mut(&c)
		}
		return &c
	}
	for _, bad := range []string{
		`t.test</script><img src=x onerror=alert(1)>`,
		"t est.test", // whitespace
		"t.test/x",   // path in hostname
		"-lead.test", // leading hyphen
		"do..ts",     // empty label
	} {
		bad := bad
		if err := base(func(c *Config) { c.Domain = bad }).normalize(); err == nil {
			t.Errorf("Domain %q must be rejected (#304 C-1)", bad)
		}
	}
	if err := base(nil).normalize(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	for _, bad := range []string{"/tu<x>", "/tunnel extra", "tunnel", "/pa$$"} {
		bad := bad
		if err := base(func(c *Config) { c.WSPath = bad }).normalize(); err == nil {
			t.Errorf("WSPath %q must be rejected (#304 C-1)", bad)
		}
	}
	if err := base(func(c *Config) { c.WSPath = "/my-tunnel" }).normalize(); err != nil {
		t.Fatalf("valid WSPath rejected: %v", err)
	}
}

// C-2: the landing page's copy button reports failure instead of claiming
// success when the clipboard is unavailable (http self-hosting).
func TestLandingCopyFailureReported(t *testing.T) {
	s := newBillingTestServer(t)
	html := landingHTML(s.cfg, "en")
	if !strings.Contains(html, "copy failed") {
		t.Fatal("clipboard failure branch must show an explicit error (#304 C-2)")
	}
	if !strings.Contains(html, "writeText(agentPrompt).then(done, failed)") {
		t.Fatal("copyAgent rejection must run the failure callback, not the success one (#304 C-2)")
	}
}

// D-1: a write that fails after its deadline passed aborts loudly even when
// the error chain does not wrap os.ErrDeadlineExceeded (h2 stream errors).
type stallWriter struct {
	deadline time.Time
}

func (w *stallWriter) Header() http.Header { return http.Header{} }
func (w *stallWriter) WriteHeader(int)     {}
func (w *stallWriter) Flush()              {}
func (w *stallWriter) SetWriteDeadline(d time.Time) error {
	w.deadline = d
	return nil
}

func (w *stallWriter) Write(b []byte) (int, error) {
	// A stalled write eats the whole refresh window before resuming.
	time.Sleep(150 * time.Millisecond)
	if !w.deadline.IsZero() && time.Now().After(w.deadline) {
		// h2-style: bare stream error, no os.ErrDeadlineExceeded wrapping.
		return 0, errors.New("http2: stream closed")
	}
	return len(b), nil
}

func TestStreamBodyAbortsOnAnyPostDeadlineWriteError(t *testing.T) {
	s := &Server{metrics: &metrics{}, cfg: Config{StreamIdleTimeout: 100 * time.Millisecond}}
	sess := newHOLSession(t)
	st := newHOLStream(t, sess, 3)

	rw := &stallWriter{}
	// Feed after the reader starts: io.Pipe writes block until consumed.
	go func() {
		if _, err := st.bodyPw.Write(bytes.Repeat([]byte{0xCD}, 64*1024)); err != nil {
			return
		}
		_ = st.bodyPw.CloseWithError(io.EOF)
	}()
	finished := make(chan any, 1)
	go func() {
		defer func() { finished <- recover() }()
		s.streamTunnelBody(rw, httptest.NewRequest(http.MethodGet, "https://dev.t.test/x", nil),
			sess, st, &protocol.Res{Type: protocol.MsgRes, StreamID: 3, Status: 200})
		finished <- nil
	}()

	select {
	case p := <-finished:
		if p != http.ErrAbortHandler {
			t.Fatalf("post-deadline write error must abort with ErrAbortHandler, got %v (#304 D-1)", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stalled write did not abort (#304 D-1)")
	}
}
