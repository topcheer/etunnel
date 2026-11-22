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
	// REAL refund shape: resource is a REFUND object - custom_id absent, the
	// capture rides links[rel=up] (#365).
	capID := "CAP-352"
	pp2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/token"):
			fmt.Fprint(w, `{"access_token":"tok","expires_in":3600}`)
		case strings.HasSuffix(r.URL.Path, "/verify-webhook-signature"):
			fmt.Fprint(w, `{"verification_status":"SUCCESS"}`)
		case strings.HasSuffix(r.URL.Path, "/orders/"+capID+"/capture"):
			fmt.Fprint(w, `{"id":"`+capID+`","custom_id":"rf352@x.com","status":"completed","purchase_units":[{"custom_id":"rf352@x.com"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer pp2.Close()
	oldClient2 := paypalHTTP
	paypalHTTP = &http.Client{Transport: rewriteTransport{base: pp2.URL}, Timeout: oldClient2.Timeout}
	resetPayPalTokenCache()
	t.Cleanup(func() { paypalHTTP = oldClient; resetPayPalTokenCache() })
	body := `{"id":"evt-352a","event_type":"PAYMENT.CAPTURE.REFUNDED","resource":{"id":"R-1","links":[{"rel":"up","href":"https://api-m.paypal.com/v2/payments/captures/` + capID + `"}]}}`
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

// #364: dispute.created carries a DISPUTE object (no receipt_email) —
// attribution must resolve via data.object.charge -> GET /v1/charges/{id}.
func TestStripeDisputeAttributionViaChargeLookup(t *testing.T) {
	s := newBillingTestServer(t)
	s.billing.StripeWebhookSecret = "whsec"
	if _, err := s.activate("dp@x.com", "pro"); err != nil {
		t.Fatal(err)
	}
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/charges/ch_364") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"ch_364","receipt_email":"dp@x.com","metadata":{"email":"dp@x.com"}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer stub.Close()
	oldBase, oldClient := stripeAPIBase, stripeHTTP
	stripeAPIBase = stub.URL
	stripeHTTP = &http.Client{Timeout: oldClient.Timeout}
	t.Cleanup(func() { stripeAPIBase, stripeHTTP = oldBase, oldClient })

	// REAL Dispute shape: charge id, empty metadata, no receipt_email.
	body := `{"id":"evt_d364","type":"charge.dispute.created","data":{"object":{"charge":"ch_364","metadata":{}}}}`
	w := httptest.NewRecorder()
	s.handleStripeWebhook(w, stripeSignedBody(t, "whsec", body))
	if w.Code != http.StatusOK {
		t.Fatalf("dispute webhook: %d", w.Code)
	}
	if a, _ := s.accounts.LookupEmail("dp@x.com"); a.Tier != "free" {
		t.Fatalf("dispute attributed via charge lookup did not roll back: %q (#364)", a.Tier)
	}
}

// #364: a PARTIAL refund keeps the entitlement (only full refunds roll back).
func TestStripePartialRefundKeepsEntitlement(t *testing.T) {
	s := newBillingTestServer(t)
	s.billing.StripeWebhookSecret = "whsec"
	if _, err := s.activate("pr@x.com", "pro"); err != nil {
		t.Fatal(err)
	}
	// amount=900, amount_refunded=90 -> partial; must NOT roll back.
	body := `{"id":"evt_p364","type":"charge.refunded","data":{"object":{"amount":900,"amount_refunded":90,"receipt_email":"pr@x.com","metadata":{}}}}`
	w := httptest.NewRecorder()
	s.handleStripeWebhook(w, stripeSignedBody(t, "whsec", body))
	if w.Code != http.StatusOK {
		t.Fatalf("partial refund webhook: %d", w.Code)
	}
	if a, _ := s.accounts.LookupEmail("pr@x.com"); a.Tier != "pro" {
		t.Fatalf("partial refund rolled back the tier (#364): %q", a.Tier)
	}
}

// #365: PayPal dispute RESOLVED in merchant favor keeps the entitlement.
func TestPayPalDisputeResolvedMerchantWinKeepsTier(t *testing.T) {
	s := newBillingTestServer(t)
	s.billing.PayPalWebhookID = "wh1"
	s.billing.PayPalClientID = "cid"
	s.billing.PayPalSecret = "sec"
	// verification (token + verify) runs before the outcome gate; stub it.
	pp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth2/token") {
			fmt.Fprint(w, `{"access_token":"tok","expires_in":3600}`)
			return
		}
		fmt.Fprint(w, `{"verification_status":"SUCCESS"}`)
	}))
	defer pp.Close()
	oldClient := paypalHTTP
	paypalHTTP = &http.Client{Transport: rewriteTransport{base: pp.URL}, Timeout: oldClient.Timeout}
	resetPayPalTokenCache()
	t.Cleanup(func() { paypalHTTP = oldClient; resetPayPalTokenCache() })
	if _, err := s.activate("mw@x.com", "pro"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.handlePayPalWebhook(w, httptest.NewRequest(http.MethodPost, "/webhooks/paypal",
		strings.NewReader(`{"id":"evt-mw","event_type":"CUSTOMER.DISPUTE.RESOLVED","resource":{"dispute_outcome":{"outcome":"seller_favor"},"disputed_transactions":[{"seller_transaction_id":"CAP-X"}]}}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("resolved webhook: %d", w.Code)
	}
	if a, _ := s.accounts.LookupEmail("mw@x.com"); a.Tier != "pro" {
		t.Fatalf("merchant-win resolution rolled back the tier (#365): %q", a.Tier)
	}
}

// #365: NOWPayments "expired" is an UNPAID lapsed invoice — no rollback.
func TestNowPaymentsExpiredKeepsTier(t *testing.T) {
	s := newBillingTestServer(t)
	s.billing.NowPaymentsIPNSecret = "ipnsecret"
	if _, err := s.activate("ne@x.com", "starter"); err != nil {
		t.Fatal(err)
	}
	ev := map[string]any{
		"payment_id":     "NP-EXP",
		"order_id":       "ne@x.com:starter",
		"payment_status": "expired",
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
		t.Fatalf("expired IPN: %d", w.Code)
	}
	if a, _ := s.accounts.LookupEmail("ne@x.com"); a.Tier != "starter" {
		t.Fatalf("expired IPN cleared a paid entitlement (#365): %q", a.Tier)
	}
}
