package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
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

func TestNowPaymentsIPNVerification(t *testing.T) {
	s := newBillingTestServer(t)
	s.billing.NowPaymentsIPNSecret = "ipnsecret"
	ev := map[string]any{"order_id": "u@x.com:starter", "payment_status": "finished"}
	// build the exact sorted-string NOWPayments signs
	keys := make([]string, 0)
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
	mac := hmac.New(sha512.New, []byte("ipnsecret"))
	mac.Write([]byte(sb.String()))
	sig := hex.EncodeToString(mac.Sum(nil))

	// valid signature -> 200 + account activated
	body, _ := json.Marshal(ev)
	r := httptest.NewRequest(http.MethodPost, "/webhooks/nowpayments", bytes.NewReader(body))
	r.Header.Set("x-nowpayments-sig", sig)
	rec := httptest.NewRecorder()
	s.handleNowPaymentsIPN(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid IPN: %d", rec.Code)
	}
	if _, ok := s.accounts.LookupEmail("u@x.com"); !ok {
		t.Fatal("IPN did not activate")
	}

	// tampered body -> 400
	r2 := httptest.NewRequest(http.MethodPost, "/webhooks/nowpayments",
		bytes.NewReader([]byte(`{"order_id":"evil@x.com:pro","payment_status":"finished"}`)))
	r2.Header.Set("x-nowpayments-sig", sig)
	rec2 := httptest.NewRecorder()
	s.handleNowPaymentsIPN(rec2, r2)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("tampered IPN accepted: %d", rec2.Code)
	}
	if _, ok := s.accounts.LookupEmail("evil@x.com"); ok {
		t.Fatal("tampered IPN activated!")
	}
}

// SECURITY regression: the public register endpoint must never mint paid tiers.
func TestRegisterForcesFreeTier(t *testing.T) {
	s := newBillingTestServer(t)
	h := s.Handler()
	for _, tc := range []struct {
		tier string
		want int
	}{
		{"pro", http.StatusPaymentRequired},
		{"starter", http.StatusPaymentRequired},
		{"", http.StatusOK},
		{"free", http.StatusOK},
	} {
		body := `{"email":"r` + tc.tier + `@x.com","tier":"` + tc.tier + `"}`
		r := httptest.NewRequest(http.MethodPost, "/api/register", strings.NewReader(body))
		r.RemoteAddr = "10.0.0." + fmt.Sprint(len(tc.tier)+len(body)%50) + ":1234" // distinct IPs: bypass per-IP register limit
		r.Header.Set("content-type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != tc.want {
			t.Fatalf("tier=%q: got %d want %d (%s)", tc.tier, rec.Code, tc.want, rec.Body.String())
		}
	}
	// any account created by register is free
	if a, ok := s.accounts.LookupEmail("r@x.com"); ok && a.Tier != "free" {
		t.Fatalf("register minted tier %q", a.Tier)
	}
}

func TestAdminFailsClosedWithoutToken(t *testing.T) {
	s := newBillingTestServer(t) // AdminToken empty
	h := s.adminHandler // same-package test
	r := httptest.NewRequest(http.MethodGet, "/admin/accounts", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unset admin token must disable the API, got %d", rec.Code)
	}
	// with a token configured: wrong token 401, right token 200
	s.cfg.AdminToken = "secret-token"
	r2 := httptest.NewRequest(http.MethodGet, "/admin/accounts", nil)
	r2.Header.Set("X-Admin-Token", "wrong")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, r2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", rec2.Code)
	}
	r3 := httptest.NewRequest(http.MethodGet, "/admin/accounts", nil)
	r3.Header.Set("X-Admin-Token", "secret-token")
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, r3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("right token: %d", rec3.Code)
	}
}
