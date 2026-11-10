package server

// Issue #39: a captured-signature webhook replayed inside the timestamp
// tolerance must not re-run activate() (which would extend ExpiresAt again).
// Issue #40: /billing/success must only activate for the buyer's session.

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func signStripe(t *testing.T, secret string, body []byte) string {
	t.Helper()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func postStripeWebhook(t *testing.T, s *Server, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(body))
	req.Header.Set("Stripe-Signature", signStripe(t, secret, []byte(body)))
	rec := httptest.NewRecorder()
	s.handleStripeWebhook(rec, req)
	return rec
}

func TestStripeWebhookReplayIsIdempotent(t *testing.T) {
	s := newBillingTestServer(t)
	s.billing.StripeWebhookSecret = "whsec_test"
	body := `{"id":"evt_replay_probe_1","type":"checkout.session.completed",` +
		`"data":{"object":{"payment_status":"paid","metadata":{"email":"r@x.com","tier":"pro"}}}}`

	if rec := postStripeWebhook(t, s, "whsec_test", body); rec.Code != http.StatusOK {
		t.Fatalf("first delivery: %d %s", rec.Code, rec.Body.String())
	}
	acct, ok := s.accounts.LookupEmail("r@x.com")
	if !ok {
		t.Fatal("first delivery did not activate")
	}
	first := acct.ExpiresAt

	// Same signed body again (well inside the ±5min tolerance): the replay
	// must be suppressed. Without #39 this renews ExpiresAt by another period.
	if rec := postStripeWebhook(t, s, "whsec_test", body); rec.Code != http.StatusOK {
		t.Fatalf("replay delivery: %d %s", rec.Code, rec.Body.String())
	}
	acct, _ = s.accounts.LookupEmail("r@x.com")
	if !acct.ExpiresAt.Equal(first) {
		t.Fatalf("replay re-activated: expires moved %v -> %v", first, acct.ExpiresAt)
	}

	// A *different* event id is a new event and still activates (renewal).
	body2 := strings.Replace(body, "evt_replay_probe_1", "evt_replay_probe_2", 1)
	if rec := postStripeWebhook(t, s, "whsec_test", body2); rec.Code != http.StatusOK {
		t.Fatalf("fresh event: %d", rec.Code)
	}
	acct, _ = s.accounts.LookupEmail("r@x.com")
	if !acct.ExpiresAt.After(first) {
		t.Fatal("fresh event did not renew")
	}
}

func TestNowPaymentsIPNReplayIsIdempotent(t *testing.T) {
	s := newBillingTestServer(t)
	s.billing.NowPaymentsIPNSecret = "ipnsecret"
	post := func(paymentID string) *httptest.ResponseRecorder {
		ev := map[string]any{
			"payment_id":     json.Number(paymentID),
			"order_id":       "n@x.com:starter",
			"payment_status": "finished",
		}
		keys := make([]string, 0, len(ev))
		for k := range ev {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var sb strings.Builder
		for i, k := range keys {
			if i > 0 {
				sb.WriteByte('&')
			}
			fmt.Fprintf(&sb, "%s=%v", k, ev[k])
		}
		raw, _ := json.Marshal(ev)
		mac := hmac.New(sha512.New, []byte("ipnsecret"))
		mac.Write([]byte(sb.String()))
		req := httptest.NewRequest(http.MethodPost, "/webhooks/nowpayments", strings.NewReader(string(raw)))
		req.Header.Set("x-nowpayments-sig", hex.EncodeToString(mac.Sum(nil)))
		rec := httptest.NewRecorder()
		s.handleNowPaymentsIPN(rec, req)
		return rec
	}

	if rec := post("4242"); rec.Code != http.StatusOK {
		t.Fatalf("first IPN: %d %s", rec.Code, rec.Body.String())
	}
	acct, ok := s.accounts.LookupEmail("n@x.com")
	if !ok {
		t.Fatal("first IPN did not activate")
	}
	first := acct.ExpiresAt

	if rec := post("4242"); rec.Code != http.StatusOK {
		t.Fatalf("replay IPN: %d", rec.Code)
	}
	acct, _ = s.accounts.LookupEmail("n@x.com")
	if !acct.ExpiresAt.Equal(first) {
		t.Fatalf("IPN replay re-activated: %v -> %v", first, acct.ExpiresAt)
	}
}

func TestPaymentActingFor(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	if _, err := s.activate("buyer@x.com", "pro"); err != nil {
		t.Fatal(err)
	}

	// no session: not acting for anyone
	if s.paymentActingFor(httptest.NewRequest(http.MethodGet, "/billing/success", nil), "buyer@x.com") {
		t.Fatal("anonymous visitor must not act for the buyer")
	}
	// someone else's session
	r := httptest.NewRequest(http.MethodGet, "/billing/success", nil)
	r.AddCookie(s.sessionCookie("other@x.com"))
	if s.paymentActingFor(r, "buyer@x.com") {
		t.Fatal("foreign session must not act for the buyer")
	}
	// the buyer
	r2 := httptest.NewRequest(http.MethodGet, "/billing/success", nil)
	r2.AddCookie(s.sessionCookie("buyer@x.com"))
	if !s.paymentActingFor(r2, "buyer@x.com") {
		t.Fatal("buyer session must act for the buyer")
	}
}

func TestBillingSuccessRejectsForeignSession(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	s.billing.StripeSecretKey = "sk_test"
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id":             "cs_test_1",
			"payment_status": "paid",
			"customer_email": "buyer@x.com",
			"metadata":       map[string]any{"tier": "pro"},
		})
	}))
	defer stub.Close()
	oldBase := stripeAPIBase
	stripeAPIBase = stub.URL
	defer func() { stripeAPIBase = oldBase }()

	// A visitor holding someone else's paid session id: refused, no account
	// side effects from this endpoint.
	r := httptest.NewRequest(http.MethodGet, "/billing/success?session_id=cs_test_1", nil)
	r.AddCookie(s.sessionCookie("attacker@x.com"))
	rec := httptest.NewRecorder()
	s.handleBillingSuccess(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign session: want 403, got %d", rec.Code)
	}
	if _, ok := s.accounts.LookupEmail("buyer@x.com"); ok {
		t.Fatal("foreign session triggered activation")
	}

	// Anonymous replay: also refused (#40 requires the buyer's session).
	rec2 := httptest.NewRecorder()
	s.handleBillingSuccess(rec2, httptest.NewRequest(http.MethodGet, "/billing/success?session_id=cs_test_1", nil))
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("anonymous visitor: want 403, got %d", rec2.Code)
	}

	// The buyer themselves: activates and shows the key page. (Their account
	// row may or may not exist yet; give it one so the session resolves.)
	if _, err := s.activate("buyer@x.com", "free"); err != nil {
		t.Fatal(err)
	}
	r3 := httptest.NewRequest(http.MethodGet, "/billing/success?session_id=cs_test_1", nil)
	r3.AddCookie(s.sessionCookie("buyer@x.com"))
	rec3 := httptest.NewRecorder()
	s.handleBillingSuccess(rec3, r3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("buyer: want 200, got %d %s", rec3.Code, rec3.Body.String())
	}
	if acct, ok := s.accounts.LookupEmail("buyer@x.com"); !ok || acct.Tier != "pro" {
		t.Fatal("buyer activation missing")
	}
}
