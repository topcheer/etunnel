package server

// #240 remainder: money-path tests for the NOWPayments trio (IPN dedup,
// success-page polling re-check, credential header), activate() tier
// validation, and the short-domain entitlement grant/reclaim lifecycle.
// (PayPal return entry + Stripe quote assembly live in
// payments_backlog_test.go from #308; refund/dispute rollback + reaper
// downgrade live in sec_debt_test.go from #313.)

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// npStub is a fake NOWPayments API: GET /v1/payment/<id> replies with the
// scripted statuses in order (the last one repeats); the x-api-key
// credential header is captured. nowpaymentsAPIBase points at it.
type npStub struct {
	statuses []string
	lastKey  string
	server   *httptest.Server
}

func newNPStub(t *testing.T, statuses []string) *npStub {
	t.Helper()
	n := &npStub{statuses: statuses}
	n.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.lastKey = r.Header.Get("x-api-key")
		idx := len(n.statuses) - 1
		if idx < 0 {
			idx = 0
		}
		st := n.statuses[idx]
		if len(n.statuses) > 1 {
			n.statuses = n.statuses[1:]
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"payment_status":%q,"actually_paid":3.0,"price_amount":3.0,"outcome_amount":3.0,"order_id":"c@x.com:starter","payment_id":"PID1"}`, st)
	}))
	t.Cleanup(n.server.Close)

	// Point the client at the stub (base is a test seam, like stripeAPIBase).
	oldBase, oldClient := nowpaymentsAPIBase, nowpayHTTP
	nowpaymentsAPIBase = n.server.URL
	nowpayHTTP = &http.Client{Timeout: oldClient.Timeout}
	t.Cleanup(func() {
		nowpaymentsAPIBase, nowpayHTTP = oldBase, oldClient
	})
	return n
}

func signedIPN(t *testing.T, s *Server, ev map[string]any) *http.Request {
	t.Helper()
	keys := make([]string, 0, len(ev))
	for k := range ev {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ { // sort: NOWPayments signs sorted k=v pairs
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte('&')
		}
		fmt.Fprintf(&sb, "%s=%v", k, ev[k])
	}
	mac := hmac.New(sha512.New, []byte(s.billing.NowPaymentsIPNSecret))
	mac.Write([]byte(sb.String()))
	body, _ := json.Marshal(ev)
	r := httptest.NewRequest(http.MethodPost, "/webhooks/nowpayments", strings.NewReader(string(body)))
	r.Header.Set("x-nowpayments-sig", hex.EncodeToString(mac.Sum(nil)))
	return r
}

// IPN dedup: the first delivery of a payment activates once and marks the
// payment seen; the replayed delivery is suppressed (200 ack, seen flag
// stays set).
func TestNowPaymentsIPNDedup(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	s.billing.NowPaymentsIPNSecret = "ipnsecret"
	if _, err := s.activate("c@x.com", "free"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resetPaymentEventDedup)
	resetPaymentEventDedup()

	ev := map[string]any{
		"payment_id":     "PID1",
		"order_id":       "c@x.com:starter",
		"payment_status": "finished",
		"actually_paid":  3.0,
		"price_amount":   3.0,
		"outcome_amount": 3.0,
	}
	w1 := httptest.NewRecorder()
	s.handleNowPaymentsIPN(w1, signedIPN(t, s, ev))
	if w1.Code != http.StatusOK {
		t.Fatalf("first IPN: %d %s", w1.Code, w1.Body.String())
	}
	if !paymentEventSeen("now:PID1") {
		t.Fatal("first delivery did not mark the payment seen")
	}
	a, _ := s.accounts.LookupEmail("c@x.com")
	if a.Tier != "starter" {
		t.Fatalf("IPN did not activate: tier=%q", a.Tier)
	}

	// Replay: suppressed (same payment id), still a 200 ack.
	w2 := httptest.NewRecorder()
	s.handleNowPaymentsIPN(w2, signedIPN(t, s, ev))
	if w2.Code != http.StatusOK {
		t.Fatalf("replayed IPN: %d", w2.Code)
	}
	if !paymentEventSeen("now:PID1") {
		t.Fatal("seen flag lost after replay")
	}
}

// Polling re-check: the success page queries the payment and activates only
// on finished-and-settled; the NOWPayments credential rides the x-api-key
// header (#240).
func TestCryptoSuccessPollingActivates(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	s.billing.NowPaymentsAPIKey = "npkey"
	if _, err := s.activate("c@x.com", "free"); err != nil {
		t.Fatal(err)
	}
	stub := newNPStub(t, []string{"finished"})

	r := httptest.NewRequest(http.MethodGet, "/billing/crypto/success/PID1", nil)
	r.AddCookie(s.sessionCookie("c@x.com"))
	w := httptest.NewRecorder()
	s.handleCryptoSuccess(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("polling success: %d %s", w.Code, w.Body.String())
	}
	a, _ := s.accounts.LookupEmail("c@x.com")
	if a.Tier != "starter" {
		t.Fatalf("polling did not activate: tier=%q", a.Tier)
	}
	if stub.lastKey != "npkey" {
		t.Fatalf("NOWPayments credential header = %q, want the configured api key (#240)", stub.lastKey)
	}
}

// A payment still settling never activates: the handler keeps polling and
// finally answers 202 with the IPN-takeover promise.
func TestCryptoSuccessPendingStaysUnsettled(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	s.billing.NowPaymentsAPIKey = "npkey"
	if _, err := s.activate("c@x.com", "free"); err != nil {
		t.Fatal(err)
	}
	newNPStub(t, []string{"confirming", "confirming", "confirming", "confirming"})

	r := httptest.NewRequest(http.MethodGet, "/billing/crypto/success/PID1", nil)
	r.AddCookie(s.sessionCookie("c@x.com"))
	w := httptest.NewRecorder()
	s.handleCryptoSuccess(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("unsettled payment: %d %s, want 202", w.Code, w.Body.String())
	}
	a, _ := s.accounts.LookupEmail("c@x.com")
	if a.Tier != "free" {
		t.Fatalf("unsettled payment activated: tier=%q", a.Tier)
	}
}

// activate() validates its inputs and the short-domain product shape.
func TestActivateValidatesTierAndProduct(t *testing.T) {
	s := newBillingTestServer(t)
	if _, err := s.activate("v@x.com", "bogus-tier"); err == nil || !strings.Contains(err.Error(), "unknown tier") {
		t.Fatalf("unknown tier not rejected: %v", err)
	}
	if _, err := s.activate("", "pro"); err == nil {
		t.Fatal("missing email not rejected")
	}
	if _, err := s.activateShortDomain("v@x.com", "short-domain-abc"); err == nil {
		t.Fatal("non-numeric short-domain product not rejected")
	}
	// Grant path: entitlement recorded, on-demand account created.
	key, err := s.activateShortDomain("n@x.com", "short-domain-2")
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	a, _ := s.accounts.LookupEmail("n@x.com")
	if a.PaidShort == nil || a.PaidShort.Len != 2 || a.APIKey != key {
		t.Fatalf("grant state wrong: %+v", a.PaidShort)
	}
}

// Short-domain reclaim: expired entitlements lose PaidShort and the 1-3
// char names they covered; longer custom domains stay.
func TestReapExpiredShortDomainsReclaims(t *testing.T) {
	s := newBillingTestServer(t)
	now := time.Now()
	if _, err := s.activate("ab@x.com", "free"); err != nil {
		t.Fatal(err)
	}
	s.accounts.Mutate("ab@x.com", func(a *Account) {
		a.Domains = []string{"ab", "longername"}
		a.PaidShort = &ShortDomain{Len: 2, ExpiresAt: now.Add(-time.Hour)}
	})
	s.reapExpiredShortDomains(now)
	a, _ := s.accounts.LookupEmail("ab@x.com")
	if a.PaidShort != nil {
		t.Fatal("expired entitlement not reclaimed (#240)")
	}
	for _, d := range a.Domains {
		if d == "ab" {
			t.Fatal("short name not released (#240)")
		}
	}
	hasLong := false
	for _, d := range a.Domains {
		if d == "longername" {
			hasLong = true
		}
	}
	if !hasLong {
		t.Fatal("longer custom domain must survive the reclaim")
	}
}
