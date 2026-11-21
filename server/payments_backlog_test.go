package server

// #240: money-path coverage - the PayPal return/activation entry (capture ->
// activate -> key page) including the OAuth credential assembly, and the
// Stripe checkout quote/session assembly (credential header + unit amount),
// plus the unconfigured-provider gates.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// rewriteTransport redirects requests to base (an http:// stub) - test-only
// stand-in for the production payment-provider endpoints.
type rewriteTransport struct{ base string }

func (t rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	host := strings.TrimPrefix(t.base, "http://")
	r2.URL.Host = host
	r2.URL.Scheme = "http"
	r2.Host = host
	return http.DefaultTransport.RoundTrip(r2)
}

func resetPayPalTokenCache() {
	ppTokMu.Lock()
	ppTok, ppTokExp, ppTokBase = "", time.Time{}, ""
	ppTokMu.Unlock()
}

const completedOrder = `{"status":"COMPLETED","purchase_units":[{"custom_id":"buyer@x.com:pro","payments":{"captures":[{"status":"COMPLETED"}]}}]}`

// PayPal return entry: token -> capture -> activate -> key page, with the
// OAuth credential assembly asserted on the wire (#240).
func TestPayPalReturnActivationEntry(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	s.billing.PayPalClientID = "cid"
	s.billing.PayPalSecret = "sec"
	buyerKey, err := s.activate("buyer@x.com", "free")
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var lastAuth string
	pp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lastAuth = r.Header.Get("Authorization")
		mu.Unlock()
		switch {
		case r.URL.Path == "/v1/oauth2/token":
			// #240 credential assembly: client_credentials grant with the
			// configured id:secret as Basic auth.
			if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("cid:sec")); lastAuth != want {
				t.Errorf("paypal oauth credential = %q, want %q", lastAuth, want)
			}
			fmt.Fprint(w, `{"access_token":"tok","expires_in":3600}`)
		case strings.HasSuffix(r.URL.Path, "/capture"):
			fmt.Fprint(w, completedOrder)
		case strings.HasSuffix(r.URL.Path, "/orders/TOK"):
			fmt.Fprint(w, completedOrder)
		default:
			http.NotFound(w, r)
		}
	}))
	defer pp.Close()

	oldClient := paypalHTTP
	paypalHTTP = &http.Client{Transport: rewriteTransport{base: pp.URL}, Timeout: oldClient.Timeout}
	resetPayPalTokenCache()
	t.Cleanup(func() {
		paypalHTTP = oldClient
		resetPayPalTokenCache()
		pp.Close()
	})

	// Entry gate: no order token -> back to pricing.
	r0 := httptest.NewRequest(http.MethodGet, "/billing/paypal-return", nil)
	w0 := httptest.NewRecorder()
	s.handlePayPalReturn(w0, r0)
	if w0.Code != http.StatusSeeOther {
		t.Fatalf("tokenless return: %d, want 303 to /pricing", w0.Code)
	}

	// Full entry: session buyer captures TOK and activates pro.
	r := httptest.NewRequest(http.MethodGet, "/billing/paypal-return?token=TOK", nil)
	r.AddCookie(s.sessionCookie("buyer@x.com"))
	w := httptest.NewRecorder()
	s.handlePayPalReturn(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("activation entry: %d %s", w.Code, w.Body.String())
	}
	snap, ok := s.accounts.Snapshot(buyerKey)
	if !ok {
		t.Fatal("buyer account vanished")
	}
	if snap.Tier != "pro" {
		t.Fatalf("PayPal entry did not activate the account: tier=%q", snap.Tier)
	}
}

// Stripe checkout: quote/session assembly - credential Basic header and the
// unit amount from tierPriceCents must reach the provider wire (#240).
func TestStripeCheckoutQuoteAssembly(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	s.billing.StripeSecretKey = "sk_test_123"
	if _, err := s.activate("s@x.com", "free"); err != nil {
		t.Fatal(err)
	}
	wantCents, buyable := s.tierPriceCents("starter")
	if !buyable {
		t.Fatal("starter not purchasable")
	}

	var mu sync.Mutex
	var lastAuth string
	var lastForm url.Values
	ss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lastAuth = r.Header.Get("Authorization")
		r.ParseForm()
		lastForm = r.PostForm
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"url":"https://checkout.stripe.com/test"}`)
	}))
	defer ss.Close()

	oldBase, oldClient := stripeAPIBase, stripeHTTP
	stripeAPIBase = ss.URL
	stripeHTTP = &http.Client{Timeout: oldClient.Timeout}
	t.Cleanup(func() { stripeAPIBase, stripeHTTP = oldBase, oldClient })

	post := func(body string, withKey bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/billing/checkout", strings.NewReader(body))
		r.AddCookie(s.sessionCookie("s@x.com"))
		w := httptest.NewRecorder()
		s.billing.StripeSecretKey = map[bool]string{true: "sk_test_123", false: ""}[withKey]
		s.handleBillingCheckout(w, r)
		return w
	}

	// Unconfigured provider gate: no key -> 503, no network call.
	if w := post(`{"tier":"starter","provider":"stripe"}`, false); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("stripe unconfigured: %d, want 503", w.Code)
	}

	// Configured: the quote (unit amount) and the credential header must be
	// assembled exactly as priced.
	w := post(`{"tier":"starter","provider":"stripe"}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("stripe checkout: %d %s", w.Code, w.Body.String())
	}
	var out map[string]string
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil || out["url"] == "" {
		t.Fatalf("checkout response missing url: %s", w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("sk_test_123:")); lastAuth != want {
		t.Errorf("stripe credential = %q, want %q", lastAuth, want)
	}
	if got := lastForm.Get("line_items[0][price_data][unit_amount]"); got != strconv.FormatInt(wantCents, 10) {
		t.Errorf("stripe quote unit_amount = %q, want %q", got, strconv.FormatInt(wantCents, 10))
	}
	if lastForm.Get("metadata[tier]") != "starter" || lastForm.Get("mode") != "payment" {
		t.Errorf("checkout session form incomplete: %v", lastForm)
	}
}
