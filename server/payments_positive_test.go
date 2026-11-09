package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Issue #17: the positive Stripe path (valid signature -> activation -> tier
// effective) had no coverage; only "bad signature -> 400" did.

func signStripe(body []byte, secret string, ts time.Time) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", ts.Unix())
	mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", ts.Unix(), hex.EncodeToString(mac.Sum(nil)))
}

func stripeEvent(email, tier, status string) []byte {
	return []byte(fmt.Sprintf(`{"type":"checkout.session.completed","data":{"object":{"payment_status":%q,"metadata":{"email":%q,"tier":%q}}}}`,
		status, email, tier))
}

func TestStripeWebhookActivatesPaidTier(t *testing.T) {
	s := newBillingTestServer(t)
	const secret = "whsec_test_secret"
	s.billing.StripeWebhookSecret = secret

	body := stripeEvent("buyer@x.com", "pro", "paid")
	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(string(body)))
	req.Header.Set("Stripe-Signature", signStripe(body, secret, time.Now()))
	rec := httptest.NewRecorder()
	s.handleStripeWebhook(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	acct, ok := s.accounts.LookupEmail("buyer@x.com")
	if !ok {
		t.Fatal("webhook did not create the account")
	}
	if acct.Tier != "pro" {
		t.Fatalf("tier = %q, want pro", acct.Tier)
	}
	if acct.APIKey == "" {
		t.Fatal("activated account has no API key")
	}
}

// An unpaid session (e.g. an async method that has not settled) must not ship.
func TestStripeWebhookIgnoresUnpaidSession(t *testing.T) {
	s := newBillingTestServer(t)
	const secret = "whsec_test_secret"
	s.billing.StripeWebhookSecret = secret

	body := stripeEvent("unpaid@x.com", "pro", "unpaid")
	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(string(body)))
	req.Header.Set("Stripe-Signature", signStripe(body, secret, time.Now()))
	rec := httptest.NewRecorder()
	s.handleStripeWebhook(rec, req)

	if _, ok := s.accounts.LookupEmail("unpaid@x.com"); ok {
		t.Fatal("unpaid checkout session created an account")
	}
}

func TestStripeWebhookRejectsForgedAndStaleSignatures(t *testing.T) {
	s := newBillingTestServer(t)
	const secret = "whsec_test_secret"
	s.billing.StripeWebhookSecret = secret
	body := stripeEvent("forged@x.com", "pro", "paid")

	cases := []struct {
		name string
		sig  string
	}{
		{"wrong secret", signStripe(body, "whsec_other", time.Now())},
		{"stale timestamp", signStripe(body, secret, time.Now().Add(-time.Hour))},
		{"missing header", ""},
		{"garbage", "t=abc,v1=zz"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(string(body)))
		if tc.sig != "" {
			req.Header.Set("Stripe-Signature", tc.sig)
		}
		rec := httptest.NewRecorder()
		s.handleStripeWebhook(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", tc.name, rec.Code)
		}
		if _, ok := s.accounts.LookupEmail("forged@x.com"); ok {
			t.Fatalf("%s: forged webhook activated an account", tc.name)
		}
	}
}

// Issue #17: accounts must survive a save/reload cycle unchanged.
func TestAccountStorePersistenceRoundtrip(t *testing.T) {
	path := t.TempDir() + "/accounts.json"
	first, err := NewAccountStore(path)
	if err != nil {
		t.Fatal(err)
	}
	acct, err := first.Create("roundtrip@x.com", "starter")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Mutate(acct.Email, func(a *Account) { a.Domains = append(a.Domains, "app") }) {
		t.Fatal("Mutate failed")
	}

	second, err := NewAccountStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := second.LookupEmail("roundtrip@x.com")
	if !ok {
		t.Fatal("account lost across restart")
	}
	if got.Tier != "starter" || got.APIKey != acct.APIKey {
		t.Fatalf("identity changed: tier=%q key=%q", got.Tier, got.APIKey)
	}
	if len(got.Domains) != 1 || got.Domains[0] != "app" {
		t.Fatalf("domains lost: %#v", got.Domains)
	}
	if got.Created.Unix() != acct.Created.Unix() {
		t.Fatalf("created timestamp changed: %v vs %v", got.Created, acct.Created)
	}
}
