package server

// #352/#353 regressions: PayPal + NOWPayments refund/chargeback states roll
// the entitlement back to free; parameter-less tap lists keep the reply text
// within Telegram's 4096-char limit with a truncation hint; admin account
// creation validates the address.

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// #352: PayPal PAYMENT.CAPTURE.REFUNDED rolls the account back to free.
func TestPayPalRefundRollsBackTier(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	s.billing.PayPalWebhookID = "wh1"
	s.billing.PayPalClientID = "cid"
	s.billing.PayPalSecret = "sec"

	var mu sync.Mutex
	calls := 0
	pp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		if strings.HasSuffix(r.URL.Path, "/oauth2/token") {
			fmt.Fprint(w, `{"access_token":"tok","expires_in":3600}`)
		} else {
			fmt.Fprint(w, `{"verification_status":"SUCCESS"}`)
		}
		mu.Unlock()
	}))
	defer pp.Close()
	oldClient := paypalHTTP
	paypalHTTP = &http.Client{Transport: rewriteTransport{base: pp.URL}, Timeout: oldClient.Timeout}
	resetPayPalTokenCache()
	t.Cleanup(func() { paypalHTTP = oldClient; resetPayPalTokenCache() })

	if _, err := s.activate("rf352@x.com", "pro"); err != nil {
		t.Fatal(err)
	}
	// #361 cascade: a live session held by the refunded account must close.
	sess, _ := seededSession(t, s, "rf352@x.com", "rf352-t1")
	body := `{"id":"evt-352a","event_type":"PAYMENT.CAPTURE.REFUNDED","resource":{"custom_id":"rf352@x.com"}}`
	r := httptest.NewRequest(http.MethodPost, "/webhooks/paypal", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handlePayPalWebhook(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("refund webhook: %d %s", w.Code, w.Body.String())
	}
	if a, _ := s.accounts.LookupEmail("rf352@x.com"); a.Tier != "free" {
		t.Fatalf("refund did not roll back tier: %q (#352)", a.Tier)
	}
	select {
	case <-sess.closed:
	default:
		t.Fatal("refund rollback did not close the holder's live session (#361 cascade)")
	}
}

// #352: PayPal CUSTOMER.DISPUTE.CREATED also rolls back.
func TestPayPalDisputeRollsBackTier(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	s.billing.PayPalWebhookID = "wh1"
	s.billing.PayPalClientID = "cid"
	s.billing.PayPalSecret = "sec"

	pp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth2/token") {
			fmt.Fprint(w, `{"access_token":"tok","expires_in":3600}`)
		} else {
			fmt.Fprint(w, `{"verification_status":"SUCCESS"}`)
		}
	}))
	defer pp.Close()
	oldClient := paypalHTTP
	paypalHTTP = &http.Client{Transport: rewriteTransport{base: pp.URL}, Timeout: oldClient.Timeout}
	resetPayPalTokenCache()
	t.Cleanup(func() { paypalHTTP = oldClient; resetPayPalTokenCache() })

	if _, err := s.activate("ds352@x.com", "starter"); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"evt-352b","event_type":"CUSTOMER.DISPUTE.CREATED","resource":{"custom_id":"ds352@x.com"}}`
	r := httptest.NewRequest(http.MethodPost, "/webhooks/paypal", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handlePayPalWebhook(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("dispute webhook: %d", w.Code)
	}
	if a, _ := s.accounts.LookupEmail("ds352@x.com"); a.Tier != "free" {
		t.Fatalf("dispute did not roll back tier: %q (#352)", a.Tier)
	}
}

// #352: NOWPayments refund IPN state rolls the account back to free.
func TestNowPaymentsRefundRollsBackTier(t *testing.T) {
	s := newBillingTestServer(t)
	s.billing.NowPaymentsIPNSecret = "ipnsecret"
	if _, err := s.activate("nr@x.com", "starter"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resetPaymentEventDedup)
	resetPaymentEventDedup()

	ev := map[string]any{
		"payment_id":     "NP352",
		"order_id":       "nr@x.com:starter",
		"payment_status": "refunded",
	}
	body, _ := json.Marshal(ev)
	keys := make([]string, 0, len(ev))
	for k := range ev {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
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
	r := httptest.NewRequest(http.MethodPost, "/webhooks/nowpayments", bytes.NewReader(body))
	r.Header.Set("x-nowpayments-sig", hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	s.handleNowPaymentsIPN(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("refund IPN: %d", w.Code)
	}
	if a, _ := s.accounts.LookupEmail("nr@x.com"); a.Tier != "free" {
		t.Fatalf("NOWPayments refund did not roll back tier: %q (#352)", a.Tier)
	}
}

// #353: parameter-less tap lists stay within the 4096-char Telegram limit
// even with 60 accounts, and carry the truncation hint.
func TestTapListBodyWithin4096(t *testing.T) {
	fake, api := newFakeTelegram(t)
	s := newTGTestServer(t, api, 42)
	for i := 0; i < 60; i++ {
		if _, err := s.accounts.Create(fmt.Sprintf("user%02d@x.com", i), "free"); err != nil {
			t.Fatal(err)
		}
	}
	for _, cmd := range []string{"/delete", "/key", "/tier"} {
		text, _ := tapDispatch(t, s, fake, cmd)
		if len(text) > 4096 {
			t.Fatalf("%s list body = %d chars, over the 4096 Telegram limit (#353)", cmd, len(text))
		}
		if !strings.Contains(text, "use the text command form for the full list") {
			t.Fatalf("%s list lacks the truncation hint (#353)", cmd)
		}
	}
}

// #353: admin account creation validates the address - drive the REAL
// handleAdminAccounts handler (the /admin/accounts POST route).
func TestAdminCreateValidatesEmail(t *testing.T) {
	s := newBillingTestServer(t)
	s.cfg.AdminToken = "tok"

	post := func(email string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/admin/accounts",
			strings.NewReader(fmt.Sprintf(`{"email":%q,"tier":"starter"}`, email)))
		r.Header.Set("X-Admin-Token", "tok")
		w := httptest.NewRecorder()
		s.handleAdminAccounts(w, r)
		return w
	}

	if w := post("not-an-email"); w.Code != http.StatusBadRequest {
		t.Fatalf("malformed email: %d %s, want 400 (#353)", w.Code, w.Body.String())
	}
	long := strings.Repeat("a", 255) + "@x.com"
	if w := post(long); w.Code != http.StatusBadRequest {
		t.Fatalf("over-long email: %d, want 400 (#353)", w.Code)
	}
	if w := post("ok@x.com"); w.Code != http.StatusNoContent && w.Code != http.StatusOK && w.Code != http.StatusConflict {
		t.Fatalf("valid email refused: %d %s", w.Code, w.Body.String())
	}
}
