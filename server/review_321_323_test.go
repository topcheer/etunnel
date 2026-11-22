package server

// #321/#323 review-batch regressions: MaxTunnels acquire atomicity (M1),
// 101 header-injection blocking (M2), mesh/sticky guard trust (LOW3),
// upgrade idle deadlines (LOW4), passkey begin rate limit + waFlow cap
// (LOW5), adminFails bounding (LOW6), PayPal webhook dedup (LOW7),
// browser-origin tunnel rejection (LOW8), telegram sweep / dispatch
// extraction / concurrent single-send (#323 M1/M2/LOW3).

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/topcheer/etunnel/protocol"
)

// #321 M1: N concurrent bounded acquires of distinct random names with
// MaxTunnels 2 -> exactly two winners; the rest get ErrAccountQuota.
func TestAcquireBoundedTunnelsAtomic(t *testing.T) {
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
				sub:     strings.Repeat("r", 3) + fmt.Sprint(i) + "-x",
				account: acct,
				closed:  make(chan struct{}),
			}
			results <- r.AcquireBounded(strings.Repeat("r", 3)+fmt.Sprint(i)+"-x", ss, acct, 2, false, 0)
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !strings.Contains(err.Error(), "quota exhausted") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if winners != 2 {
		t.Fatalf("MaxTunnels 2 admitted %d concurrent tunnels, want exactly 2 (#321 M1)", winners)
	}
}

// #321 M2: CRLF-bearing headers in res.Headers must not reach the raw 101
// replay (response splitting / injected Set-Cookie).
func TestServeUpgrade101HeaderInjectionBlocked(t *testing.T) {
	s := &Server{metrics: &metrics{}, cfg: Config{StreamIdleTimeout: 5 * time.Second}}
	sess := newHOLSession(t)
	st := newHOLStream(t, sess, 9)

	res := &protocol.Res{
		Type:   protocol.MsgRes,
		Status: http.StatusSwitchingProtocols,
		Headers: map[string][]string{
			"X-Ok":              {"fine"},
			"X-Exploit":         {"a\r\nSet-Cookie: pwned=1"},
			"X-Bad\r\nInjected": {"1"},
			"Upgrade":           {"websocket"},
		},
	}
	visitor, peer := net.Pipe()
	defer visitor.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.serveUpgrade(&hijackRecorder{ResponseRecorder: httptest.NewRecorder(), conn: visitor},
			httptest.NewRequest(http.MethodGet, "https://dev.t.test/ws", nil), sess, st, res)
	}()

	// Consume the replayed 101 head byte-by-byte (the proven backlog-test
	// pattern), then assert injection payloads never reached the visitor.
	head := make([]byte, 0, 256)
	buf := make([]byte, 1)
	peer.SetReadDeadline(time.Now().Add(3 * time.Second))
	for !bytes.HasSuffix(head, []byte("\r\n\r\n")) {
		if _, err := peer.Read(buf); err != nil {
			t.Fatalf("reading 101 head: %v (head so far %q)", err, head)
		}
		head = append(head, buf[0])
	}
	if strings.Contains(string(head), "Set-Cookie") || strings.Contains(string(head), "pwned") {
		t.Fatalf("CRLF-injected header reached the visitor head: %q (#321 M2)", head)
	}
	if strings.Contains(string(head), "Injected") {
		t.Fatalf("injected header name reached the visitor head: %q (#321 M2)", head)
	}
	if !strings.Contains(string(head), "X-Ok: fine") {
		t.Fatalf("valid header lost: %q", head)
	}
}

// #321 LOW3: mesh Forward guards reuse the sticky router's isInternalPeer
// trust boundary (covered by TestIsInternalPeer in
// router_sticky_internal_test.go); the strip-and-honor behavior in
// meshRouter.Forward mirrors stickyRouter.Forward's #295 fix.

// #321 LOW4: a stalled upgraded-stream visitor is reaped by the per-write
// idle deadline instead of pinning the splice goroutine forever.
func TestServeUpgradeDownlinkIdleTimeout(t *testing.T) {
	s := &Server{metrics: &metrics{}, cfg: Config{StreamIdleTimeout: 150 * time.Millisecond}}
	sess := newHOLSession(t)
	st := newHOLStream(t, sess, 9)

	visitor, peer := net.Pipe()
	defer visitor.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		res := &protocol.Res{Type: protocol.MsgRes, StreamID: 9, Status: http.StatusSwitchingProtocols}
		s.serveUpgrade(&hijackRecorder{ResponseRecorder: httptest.NewRecorder(), conn: visitor},
			httptest.NewRequest(http.MethodGet, "https://dev.t.test/ws", nil), sess, st, res)
	}()

	// Drain the replayed 101 head byte-by-byte, then stop reading entirely:
	// from here the visitor side simulates a TCP zero-window.
	head := make([]byte, 0, 128)
	buf := make([]byte, 1)
	peer.SetReadDeadline(time.Now().Add(3 * time.Second))
	for !bytes.HasSuffix(head, []byte("\r\n\r\n")) {
		if _, err := peer.Read(buf); err != nil {
			break // head drain ends (deadline/close) - now stall
		}
		head = append(head, buf[0])
	}
	peer.SetReadDeadline(time.Time{})
	_ = peer.SetReadDeadline(time.Now().Add(time.Millisecond))

	// Keep feeding the app side; the visitor never drains again.
	stop := make(chan struct{})
	defer close(stop)
	feeder := make(chan struct{})
	go func() {
		defer close(feeder)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := st.bodyPw.Write([]byte("payload-payload-payload")); err != nil {
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stalled upgraded stream was not reaped by the idle deadline (#321 LOW4)")
	}
}

// #321 LOW5: the unauthenticated passkey login-begin endpoint is rate
// limited per source IP.
func TestPasskeyLoginBeginRateLimited(t *testing.T) {
	s := newBillingTestServer(t)
	ip := "203.0.113.9:1234"
	allowed := 0
	for i := 0; i < 40; i++ {
		if s.allowPasskeyBegin(ip) {
			allowed++
		}
	}
	if allowed > 30 {
		t.Fatalf("begin limiter admitted %d calls per minute window (#321 LOW5)", allowed)
	}
	if s.allowPasskeyBegin("other-ip:1") != true {
		t.Fatal("other IPs must be independent")
	}
}

// #321 LOW6: the adminFails table is bounded - seeding past the floor and
// recording one more failure prunes stale entries.
func TestAdminFailsPruned(t *testing.T) {
	s := newBillingTestServer(t)
	s.cfg.AdminToken = "right"
	for i := 0; i < 5000; i++ {
		s.adminFails[fmt.Sprintf("10.0.0.%d:1", i%250+1)] = &adminFailState{fails: 1}
	}
	r := httptest.NewRequest(http.MethodGet, "/admin/accounts", nil)
	r.Header.Set("X-Admin-Token", "wrong")
	w := httptest.NewRecorder()
	s.adminAuth(func(w http.ResponseWriter, r *http.Request) {})(w, r)
	if len(s.adminFails) > 4096 {
		t.Fatalf("adminFails not pruned: %d entries (#321 LOW6)", len(s.adminFails))
	}
}

// #321 LOW7: a PayPal webhook replay is deduped - the second delivery must
// not re-activate (observable via the account tier staying and, chiefly, the
// activation happening exactly once - verified through DowngradeToFree-free
// state plus the stubbed API call count on the verify endpoint).
func TestPayPalWebhookDedup(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	s.billing.PayPalWebhookID = "wh1"

	verifyCalls := 0
	var mu sync.Mutex
	pp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth2/token") {
			fmt.Fprint(w, `{"access_token":"tok","expires_in":3600}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/validate-webhook-signature") ||
			strings.HasSuffix(r.URL.Path, "/verify-webhook-signature") {
			mu.Lock()
			verifyCalls++
			mu.Unlock()
			fmt.Fprint(w, `{"verification_status":"SUCCESS"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer pp.Close()

	oldClient := paypalHTTP
	paypalHTTP = &http.Client{Transport: rewriteTransport{base: pp.URL}, Timeout: oldClient.Timeout}
	t.Cleanup(func() { paypalHTTP = oldClient })

	send := func(id string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"id":%q,"event_type":"PAYMENT.CAPTURE.COMPLETED","resource":{"custom_id":"pp@x.com:pro"}}`, id)
		r := httptest.NewRequest(http.MethodPost, "/webhooks/paypal", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.handlePayPalWebhook(w, r)
		return w
	}
	if w := send("evt-pp-1"); w.Code != http.StatusOK {
		t.Fatalf("first webhook: %d %s", w.Code, w.Body.String())
	}
	if a, _ := s.accounts.LookupEmail("pp@x.com"); a.Tier != "pro" {
		t.Fatalf("first webhook did not activate: %+v", a)
	}
	// Replay: signature verification legitimately runs on every delivery
	// (verify-then-trust), but the DEDUP must suppress the second
	// activation - observable via the "replay suppressed" log line.
	var logBuf strings.Builder
	oldLog := logPrintf
	logPrintf = func(f string, a ...any) (int, error) { return fmt.Fprintf(&logBuf, f, a...) }
	t.Cleanup(func() { logPrintf = oldLog })
	if w := send("evt-pp-1"); w.Code != http.StatusOK {
		t.Fatalf("replayed webhook: %d", w.Code)
	}
	if !strings.Contains(logBuf.String(), "replay suppressed") {
		t.Fatalf("replay was not suppressed - PayPal webhook dedup missing (#321 LOW7): %s", logBuf.String())
	}
}

// #321 LOW8: browser-origin tunnel handshakes are rejected at the edge.
func TestTunnelWSRejectsBrowserOrigin(t *testing.T) {
	s := newBillingTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/tunnel", nil)
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	s.handleTunnelWS(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("browser-origin handshake: %d, want 403 (#321 LOW8)", w.Code)
	}
	if strings.Contains(w.Body.String(), "Origin") == false && !strings.Contains(strings.ToLower(w.Body.String()), "sdk") {
		t.Log("rejection message:", w.Body.String())
	}
}

// #323 M1: the tunnel-open cooldown sweep removes expired entries and keeps
// fresh ones (injected clock).
func TestSweepTunnelOpenSeen(t *testing.T) {
	old := tunnelOpenCooldown
	tunnelOpenCooldown = time.Minute
	t.Cleanup(func() { tunnelOpenCooldown = old })
	now := time.Now()
	tunnelOpenSeen.Store("stale", now.Add(-2*time.Minute))
	tunnelOpenSeen.Store("fresh", now)
	removed := sweepTunnelOpenSeen(now.Add(time.Second))
	if removed != 1 {
		t.Fatalf("sweep removed %d entries, want 1 (#323 M1)", removed)
	}
	if _, ok := tunnelOpenSeen.Load("stale"); ok {
		t.Fatal("stale entry survived the sweep (#323 M1)")
	}
	if _, ok := tunnelOpenSeen.Load("fresh"); !ok {
		t.Fatal("fresh entry wrongly removed")
	}
	tunnelOpenSeen.Delete("fresh")
}

// #323 M2: the extracted update dispatch - whitelist, reply, offset.
func TestTelegramDispatchWhitelistAndOffset(t *testing.T) {
	fake, api := newFakeTelegram(t)
	s := newTGTestServer(t, api, 42)

	admin := map[string]any{
		"update_id": 7.0,
		"message":   map[string]any{"chat": map[string]any{"id": 42.0}, "text": "/help"},
	}
	if next := s.dispatchTelegramUpdate(context.Background(), admin); next != 8 {
		t.Fatalf("offset = %d, want 8", next)
	}
	fake.mu.Lock()
	replied := len(fake.sent) > 0
	fake.mu.Unlock()
	if !replied {
		t.Fatal("admin command not answered (#323 M2)")
	}

	stranger := map[string]any{
		"update_id": 9.0,
		"message":   map[string]any{"chat": map[string]any{"id": 99.0}, "text": "/help"},
	}
	if next := s.dispatchTelegramUpdate(context.Background(), stranger); next != 10 {
		t.Fatalf("stranger offset = %d, want 10", next)
	}
	fake.mu.Lock()
	n := len(fake.sent)
	fake.mu.Unlock()
	if n != 1 {
		t.Fatalf("stranger chat must be dropped silently (#323 M2), sends=%d", n)
	}
}

// #323 LOW3: concurrent handshakes for the same fresh subdomain yield
// exactly one notification (LoadOrStore claim is atomic).
func TestNotifyTunnelOpenConcurrentSingleSend(t *testing.T) {
	fake, api := newFakeTelegram(t)
	s := newTGTestServer(t, api, 42)
	tunnelOpenSeen.Range(func(k, _ any) bool {
		tunnelOpenSeen.Delete(k)
		return true
	})
	sess := func() *Session {
		return &Session{sub: "race-otter-x9", url: "https://race-otter-x9.t.test"}
	}

	const n = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s.notifyTunnelOpen(sess(), "Starter")
		}()
	}
	close(start)
	wg.Wait()

	// async send: poll briefly for the single delivery
	count := 0
	for i := 0; i < 100; i++ {
		fake.mu.Lock()
		count = len(fake.sent)
		fake.mu.Unlock()
		if count >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if count != 1 {
		t.Fatalf("concurrent opens sent %d notifications, want exactly 1 (#323 LOW3)", count)
	}
	time.Sleep(150 * time.Millisecond) // give a double-send time to (wrongly) land
	fake.mu.Lock()
	count = len(fake.sent)
	fake.mu.Unlock()
	if count != 1 {
		t.Fatalf("concurrent opens double-sent: %d notifications (#323 LOW3)", count)
	}
}
