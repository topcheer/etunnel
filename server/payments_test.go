package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStripeVerifySignature(t *testing.T) {
	secret := "whsec_test"
	body := []byte(`{"type":"checkout.session.completed"}`)
	ts := fmt.Sprint(time.Now().Unix())
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))
	hdr := "t=" + ts + ",v1=" + sig

	if !stripeVerifySignature(body, hdr, secret, 5*time.Minute) {
		t.Fatal("valid signature rejected")
	}
	if stripeVerifySignature(body, "t="+ts+",v1="+strings.Repeat("0", 64), secret, 5*time.Minute) {
		t.Fatal("wrong signature accepted")
	}
	if stripeVerifySignature(body, hdr, "whsec_other", 5*time.Minute) {
		t.Fatal("wrong secret accepted")
	}
	old := fmt.Sprint(time.Now().Add(-10 * time.Minute).Unix())
	if stripeVerifySignature(body, "t="+old+",v1="+sig, secret, 5*time.Minute) {
		t.Fatal("stale timestamp accepted")
	}
	if stripeVerifySignature([]byte("tampered"), hdr, secret, 5*time.Minute) {
		t.Fatal("tampered body accepted")
	}
}

func newBillingTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := Config{
		Domain:       "t.test",
		Addr:         "127.0.0.1:1",
		TLS:          TLSConfig{Mode: TLSModeHTTP},
		AccountsFile: filepath.Join(t.TempDir(), "a.json"),
		Tiers:        DefaultTiers(),
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestActivateCreateAndRenew(t *testing.T) {
	s := newBillingTestServer(t)
	key1, err := s.activate("a@x.com", "pro")
	if err != nil || key1 == "" {
		t.Fatalf("create: %v", err)
	}
	// renewal keeps the key, bumps the tier
	acct, ok := s.accounts.LookupEmail("a@x.com")
	if !ok || acct.Tier != "pro" {
		t.Fatalf("store state: %+v", acct)
	}
	key2, err := s.activate("a@x.com", "starter")
	if err != nil {
		t.Fatal(err)
	}
	if key2 != key1 {
		t.Fatal("renewal must keep the same API key")
	}
	acct, _ = s.accounts.LookupEmail("a@x.com")
	if acct.Tier != "starter" {
		t.Fatalf("tier not updated: %s", acct.Tier)
	}
	if _, err := s.activate("b@x.com", "nope"); err == nil {
		t.Fatal("unknown tier must fail")
	}
}

func TestPricingAndCheckout(t *testing.T) {
	s := newBillingTestServer(t)

	// pricing page renders purchasable rows
	rec := httptest.NewRecorder()
	s.handlePricing(rec, httptest.NewRequest(http.MethodGet, "/pricing", nil))
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "Starter") || !strings.Contains(body, "PayPal") {
		t.Fatalf("pricing page: %d %.120s", rec.Code, body)
	}

	// checkout without a login session is rejected (401) before anything else
	for _, payload := range []string{
		`{"email":"a@x.com","tier":"pro","provider":"stripe"}`,
		`{"email":"a@x.com","tier":"pro","provider":"paypal"}`,
	} {
		rec = httptest.NewRecorder()
		s.handleBillingCheckout(rec, httptest.NewRequest(http.MethodPost, "/api/billing/checkout",
			strings.NewReader(payload)))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("logged-out checkout %s: want 401, got %d", payload, rec.Code)
		}
	}
	// bad provider / bad tier / bad email
	for _, payload := range []string{
		`{"email":"a@x.com","tier":"pro","provider":"alipay"}`,
		`{"email":"a@x.com","tier":"gold","provider":"stripe"}`,
		`{"email":"nope","tier":"pro","provider":"stripe"}`,
	} {
		rec = httptest.NewRecorder()
		s.handleBillingCheckout(rec, httptest.NewRequest(http.MethodPost, "/api/billing/checkout",
			strings.NewReader(payload)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("payload %s: want 400, got %d", payload, rec.Code)
		}
	}
	// env-driven price override reflects in page
	t.Setenv("TIER_PRO_PRICE_CENTS", "1234")
	rec = httptest.NewRecorder()
	s.handlePricing(rec, httptest.NewRequest(http.MethodGet, "/pricing", nil))
	if !strings.Contains(rec.Body.String(), "$12.34/mo") {
		t.Fatal("price override not rendered")
	}
}

func TestStripeWebhookRejectsBadSignature(t *testing.T) {
	s := newBillingTestServer(t)
	s.billing.StripeWebhookSecret = "whsec_x"
	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe",
		strings.NewReader(`{"type":"checkout.session.completed"}`))
	req.Header.Set("Stripe-Signature", "t=1,v1=deadbeef")
	rec := httptest.NewRecorder()
	s.handleStripeWebhook(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad signature: want 400, got %d", rec.Code)
	}
}

func TestPayPalOrderMeta(t *testing.T) {
	e, tier := paypalOrderMeta(map[string]any{
		"purchase_units": []any{map[string]any{"custom_id": "u@x.com:pro"}},
	})
	if e != "u@x.com" || tier != "pro" {
		t.Fatalf("got %q %q", e, tier)
	}
}

var _ = os.Getenv

// Capture responses sometimes omit purchase_units.custom_id; the parser
// must yield empty strings then (triggering the GET-order fallback) and
// parse the GET shape correctly.
func TestPayPalOrderMetaCaptureVsGetShapes(t *testing.T) {
	captureShape := map[string]any{ // no custom_id back from capture
		"id": "X", "status": "COMPLETED",
		"purchase_units": []any{map[string]any{
			"payments": map[string]any{"captures": []any{map[string]any{"status": "COMPLETED"}}}}},
	}
	if e, tier := paypalOrderMeta(captureShape); e != "" || tier != "" {
		t.Fatalf("capture shape should yield empty meta, got %q %q", e, tier)
	}
	getShape := map[string]any{
		"purchase_units": []any{map[string]any{"custom_id": "u@x.com:pro"}},
	}
	if e, tier := paypalOrderMeta(getShape); e != "u@x.com" || tier != "pro" {
		t.Fatalf("GET shape meta: %q %q", e, tier)
	}
}
