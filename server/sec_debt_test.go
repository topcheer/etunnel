package server

// #295 debt-batch regressions: session epoch revocation (1), Stripe
// refund/chargeback rollback (2), sticky loop-guard trust boundary (3),
// "me" reserved subdomain (5), admin-token brute-force cooldown (6), and
// no internal-detail echo on activation failures (8).

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/topcheer/etunnel/protocol"
)

// 1: logout and API-key reset bump the session epoch - cookies minted
// before the event stop verifying, cookies minted after it work.
func TestSessionEpochRevocation(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	if _, err := s.activate("e1@x.com", "pro"); err != nil {
		t.Fatal(err)
	}
	emailReq := func(cookie *http.Cookie) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/me", nil)
		r.AddCookie(cookie)
		return r
	}

	c1 := s.sessionCookie("e1@x.com")
	if got := s.sessionEmail(emailReq(c1)); got != "e1@x.com" {
		t.Fatalf("baseline session lost: %q", got)
	}

	// Logout bumps the epoch: the old cookie must die.
	lw := httptest.NewRecorder()
	lr := emailReq(c1)
	s.handleLogout(lw, lr)
	if got := s.sessionEmail(emailReq(c1)); got != "" {
		t.Fatalf("cookie survived logout (#295 item 1)")
	}
	c2 := s.sessionCookie("e1@x.com")
	if got := s.sessionEmail(emailReq(c2)); got != "e1@x.com" {
		t.Fatalf("fresh post-logout session rejected: %q", got)
	}

	// API-key reset bumps again: same revocation semantics.
	if w := authedReq(t, s, "e1@x.com", "/me/reset", `{"email":"e1@x.com"}`); w.Code != 200 {
		t.Fatalf("reset: %d %s", w.Code, w.Body.String())
	}
	if got := s.sessionEmail(emailReq(c2)); got != "" {
		t.Fatal("cookie survived API-key reset (#295 item 1)")
	}
	c3 := s.sessionCookie("e1@x.com")
	if got := s.sessionEmail(emailReq(c3)); got != "e1@x.com" {
		t.Fatalf("fresh post-reset session rejected: %q", got)
	}
}

func stripeSignedEvent(t *testing.T, s *Server, body string) *http.Request {
	t.Helper()
	ts := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(s.billing.StripeWebhookSecret))
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write([]byte(body))
	r := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(body))
	r.Header.Set("Stripe-Signature", fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil))))
	return r
}

// 2: charge.refunded rolls the account back to free and revokes the premium
// short-domain entitlement, mirroring the tier reaper.
func TestStripeRefundRollsBackTier(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	s.billing.StripeWebhookSecret = "whsec"
	if _, err := s.activate("rf@x.com", "pro"); err != nil {
		t.Fatal(err)
	}
	snap0, ok := s.accounts.LookupEmail("rf@x.com")
	if !ok {
		t.Fatal("account vanished")
	}
	buyerKey := snap0.APIKey
	s.accounts.Mutate("rf@x.com", func(a *Account) {
		a.PaidShort = &ShortDomain{Len: 3, ExpiresAt: time.Now().Add(30 * 24 * time.Hour)}
	})

	body := `{"id":"evt_ref1","type":"charge.refunded","data":{"object":{"metadata":{"email":"rf@x.com"}}}}`
	w := httptest.NewRecorder()
	s.handleStripeWebhook(w, stripeSignedEvent(t, s, body))
	if w.Code != http.StatusOK {
		t.Fatalf("refund webhook: %d %s", w.Code, w.Body.String())
	}
	snap, ok := s.accounts.Snapshot(buyerKey)
	if !ok {
		t.Fatal("buyer account vanished")
	}
	if snap.Tier != "free" {
		t.Fatalf("refund did not roll back tier: %q (#295 item 2)", snap.Tier)
	}
	if snap.PaidShort != nil {
		t.Fatal("refund did not revoke the short-domain entitlement (#295 item 2)")
	}
	// Disputes roll back the same way; the event id dedupes separately.
	body2 := strings.Replace(body, `"id":"evt_ref1"`, `"id":"evt_dsp1"`, 1)
	body2 = strings.Replace(body2, "charge.refunded", "charge.dispute.created", 1)
	w2 := httptest.NewRecorder()
	s.handleStripeWebhook(w2, stripeSignedEvent(t, s, body2))
	if w2.Code != http.StatusOK {
		t.Fatalf("dispute webhook: %d", w2.Code)
	}
}

// 3: the sticky forward loop-guard header is only trusted from internal
// peers - a public visitor forging it must not force a routing failure.
func TestStickyForwardGuardTrustsInternalOnly(t *testing.T) {
	r := newTestSticky(okSet,
		func(context.Context, string) (string, error) { return "127.0.0.1:1", nil },
		okDel)

	req := func(remote, fwd string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "https://v.t.test/ws", nil)
		r.RemoteAddr = remote
		if fwd != "" {
			r.Header.Set("Connection", "Upgrade")
			r.Header.Set("Upgrade", "websocket")
			r.Header.Set("X-Etunnel-Forward", fwd)
		}
		return r
	}

	// Public visitor forging the guard: must NOT fail routing - it proceeds
	// to the (refused) owner and reports handled. The sink conn swallows
	// the raw 502 that forwardUpgraded writes post-hijack.
	visitor, visitorPeer := net.Pipe()
	defer visitor.Close()
	defer visitorPeer.Close()
	go func() { _, _ = io.Copy(io.Discard, visitorPeer) }() // drain the 502
	hj := &hijackRecorder{ResponseRecorder: httptest.NewRecorder(), conn: visitor}
	if handled := r.Forward("sub9", hj, req("203.0.113.9:5555", "peer-x")); !handled {
		t.Fatal("forged loop-guard header forced a routing failure (#295 item 3)")
	}
	// Internal peer carrying the guard: loop protection still applies.
	hj2 := &hijackRecorder{ResponseRecorder: httptest.NewRecorder(), conn: visitor}
	if handled := r.Forward("sub9", hj2, req("10.1.2.3:5000", "peer-x")); handled {
		t.Fatal("internal loop guard must still stop the forward (#295 item 3)")
	}
}

// 5: "me" is a reserved subdomain (phishing lookalike of the portal host).
func TestReservedMeSubdomain(t *testing.T) {
	if err := protocol.ValidateSubdomain("me"); err == nil {
		t.Fatal("me must be reserved (#295 item 5)")
	}
	if err := protocol.ValidateSubdomain("www"); err == nil {
		t.Fatal("www positive control failed")
	}
	if err := protocol.ValidateSubdomain("meadow"); err != nil {
		t.Fatalf("meadow must stay registrable: %v", err)
	}
}

// 6: after 5 bad admin tokens from one IP, every attempt waits out the
// cooldown - even with the correct token.
func TestAdminTokenBruteForceCooldown(t *testing.T) {
	s := newBillingTestServer(t)
	s.cfg.AdminToken = "right"
	old := adminFailCooldown
	adminFailCooldown = 60 * time.Millisecond
	t.Cleanup(func() { adminFailCooldown = old })

	hit := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/admin/accounts", nil)
		r.Header.Set("X-Admin-Token", token)
		w := httptest.NewRecorder()
		s.adminAuth(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })(w, r)
		return w
	}
	for i := 0; i < 5; i++ {
		if w := hit("wrong"); w.Code != http.StatusUnauthorized {
			t.Fatalf("bad token #%d: %d, want 401", i+1, w.Code)
		}
	}
	if w := hit("right"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("correct token during cooldown: %d, want 429 (#295 item 6)", w.Code)
	}
	time.Sleep(80 * time.Millisecond)
	if w := hit("right"); w.Code != http.StatusOK {
		t.Fatalf("correct token after cooldown: %d, want 200", w.Code)
	}
}

// 8: activation failures answer generically - internal detail goes to the
// log, not the buyer's screen.
func TestActivationErrorNotEchoed(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	s.billing.StripeWebhookSecret = "whsec"
	if _, err := s.activate("e8@x.com", "free"); err != nil {
		t.Fatal(err)
	}

	var logBuf strings.Builder
	old := logPrintf
	logPrintf = func(f string, a ...any) (int, error) { return fmt.Fprintf(&logBuf, f, a...) }
	t.Cleanup(func() { logPrintf = old })

	// checkout.session.completed with an unknown tier -> activate errors
	// with "unknown tier"; the response must not carry it.
	body := `{"id":"evt_e81","type":"checkout.session.completed","data":{"object":{"payment_status":"paid","metadata":{"email":"e8@x.com","tier":"bogus-tier"}}}}`
	w := httptest.NewRecorder()
	s.handleStripeWebhook(w, stripeSignedEvent(t, s, body))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("webhook: %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "bogus-tier") || strings.Contains(w.Body.String(), "unknown tier") {
		t.Fatalf("internal activation detail echoed to the client (#295 item 8): %s", w.Body.String())
	}
	if !strings.Contains(logBuf.String(), "unknown tier") {
		t.Fatal("activation detail must still reach the server log (#295 item 8)")
	}
}
