package server

// Billing: PayPal + Stripe integration, zero third-party dependencies.
//
// User site:  GET  /pricing                      - plan picker (email + pay)
//             POST /api/billing/checkout         - create a payment session
//             GET  /billing/success              - post-pay landing (shows key)
// Webhooks:   POST /webhooks/stripe              - signature-verified activation
//             POST /webhooks/paypal              - verified backup activation
// Admin:      payment status folded into /stats.
//
// Activation = AccountStore.Create/SetTier(email, tier): the API key is the
// product; first purchase mints it, renewals keep it and bump the tier.

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BillingConfig holds provider credentials; empty fields disable a provider.
type BillingConfig struct {
	StripeSecretKey      string // sk_live_... / sk_test_...
	StripeWebhookSecret  string // whsec_...
	PayPalClientID       string
	PayPalSecret         string
	PayPalEnv            string // "live" or "sandbox" (default live)
	PayPalWebhookID      string // for webhook verification
	NowPaymentsAPIKey    string // crypto checkout (nowpayments.io)
	NowPaymentsIPNSecret string
	PublicBaseURL        string // e.g. https://t.example.com (defaults to https://<Domain>)
}

func billingFromEnv(domain string) BillingConfig {
	base := os.Getenv("ETUNNEL_PUBLIC_URL")
	if base == "" {
		base = "https://" + domain
	}
	return BillingConfig{
		StripeSecretKey:      os.Getenv("STRIPE_SECRET_KEY"),
		StripeWebhookSecret:  os.Getenv("STRIPE_WEBHOOK_SECRET"),
		PayPalClientID:       os.Getenv("PAYPAL_CLIENT_ID"),
		PayPalSecret:         os.Getenv("PAYPAL_SECRET"),
		PayPalEnv:            os.Getenv("PAYPAL_ENV"),
		PayPalWebhookID:      os.Getenv("PAYPAL_WEBHOOK_ID"),
		NowPaymentsAPIKey:    os.Getenv("NOWPAYMENTS_API_KEY"),
		NowPaymentsIPNSecret: os.Getenv("NOWPAYMENTS_IPN_SECRET"),
		PublicBaseURL:        base,
	}
}

// tierPrice is the monthly price in cents for purchasable tiers.
// Overridable via TIER_<ID>_PRICE_CENTS env (e.g. TIER_PRO_PRICE_CENTS=900).
func (s *Server) tierPriceCents(tierID string) (int64, bool) {
	if v := os.Getenv("TIER_" + strings.ToUpper(tierID) + "_PRICE_CENTS"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err == nil && n > 0 {
			return n, true
		}
	}
	builtin := map[string]int64{"starter": 300, "pro": 900}
	n, ok := builtin[tierID]
	return n, ok
}

// --------------------------------------------------------------------------
// routes

func (s *Server) handlePricing(w http.ResponseWriter, r *http.Request) {
	type row struct {
		ID, Name, Price string
		TTL             string
		Custom          int
		BandwidthMbps   int
		Buyable         bool
	}
	var rows []row
	ids := make([]string, 0, len(s.cfg.Tiers))
	for id := range s.cfg.Tiers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		t := s.cfg.Tiers[id]
		cents, buyable := s.tierPriceCents(id)
		p := ""
		if buyable {
			p = fmt.Sprintf("$%d.%02d/mo", cents/100, cents%100)
		}
		rows = append(rows, row{ID: id, Name: t.Name, Price: p,
			TTL: t.TTL.String(), Custom: t.CustomDomains,
			BandwidthMbps: t.Bandwidth / 1_000_000, Buyable: buyable})
	}
	const pricingHTML = `<!doctype html><html><head><meta charset="utf-8">
<link rel="icon" href="data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 512 512' width='512' height='512' role='img' aria-label='etunnel'>  <rect width='512' height='512' rx='96' fill='#0d1117'/>  <rect x='120' y='128' width='272' height='256' rx='40' fill='none' stroke='#e6edf3' stroke-width='40'/>  <line x1='48' y1='256' x2='464' y2='256' stroke='#3fb950' stroke-width='40' stroke-linecap='round'/></svg>"><title>{{.Domain}} - pricing</title>
<style>body{font-family:system-ui,sans-serif;background:#0b0e14;color:#e6edf3;max-width:820px;margin:2rem auto;padding:0 1rem}
table{border-collapse:collapse;width:100%;margin:1.5rem 0}
th,td{padding:.6rem .8rem;border-bottom:1px solid #21262d;text-align:left}
.buy{background:#238636;color:#fff;border:0;border-radius:6px;padding:.45rem .9rem;font-size:1rem;cursor:pointer}
input{background:#161b22;color:#e6edf3;border:1px solid #30363d;border-radius:6px;padding:.5rem;width:16rem}
.paypal{background:#ffc439;color:#111}label{display:block;margin:.6rem 0 .2rem;color:#8b949e}
#msg{margin-top:1rem;min-height:1.4rem}</style></head><body>
<h1><svg width="40" height="40" viewBox="0 0 512 512" aria-label="etunnel" style="vertical-align:middle;margin-right:.6rem"><rect width="512" height="512" rx="96" fill="#0d1117"/><rect x="120" y="128" width="272" height="256" rx="40" fill="none" stroke="#e6edf3" stroke-width="40"/><line x1="48" y1="256" x2="464" y2="256" stroke="#3fb950" stroke-width="40" stroke-linecap="round"/></svg>{{.Domain}} - plans</h1>
{{if .User}}<p style="background:#161b22;border:1px solid #30363d;border-radius:6px;padding:.6rem;color:#3fb950">
  ✓ Signed in as <b>{{.User}}</b> &middot; <a href="/me" style="color:#58a6ff">my account</a> &middot; <a href="/logout" style="color:#8b949e">sign out</a></p>
{{else}}<p style="background:#161b22;border:1px solid #30363d;border-radius:6px;padding:.6rem;color:#d29922">
  Not signed in - <a href="/login" style="color:#58a6ff">sign in first</a> to subscribe.</p>{{end}}
<p>Pay once, get an API key instantly. Manage tunnels with it (custom subdomains, longer TTL).</p>
<label for="email">Email (your account identity)</label>
<input id="email" type="email" placeholder="you@example.com" autocomplete="email" value="{{.User}}"{{if .User}} readonly style="opacity:.7"{{end}}>
<table><tr><th>Plan</th><th>TTL</th><th>Custom subdomains</th><th>Bandwidth</th><th>Price</th><th></th></tr>
{{range .Rows}}{{if .Buyable}}<tr><td><b>{{.Name}}</b></td><td>{{.TTL}}</td><td>{{.Custom}}</td>
<td>{{.BandwidthMbps}} Mbps</td><td>{{.Price}}</td>
<td><button class="buy" onclick="pay(this,'{{.ID}}','crypto')">💎 Crypto</button>
<button class="buy paypal" onclick="pay(this,'{{.ID}}','paypal')">PayPal</button></td></tr>{{end}}{{end}}
</table>
<div id="msg"></div>
<script>
async function pay(btn, tier, provider){
  const email = document.getElementById('email').value.trim();
  const msg = document.getElementById('msg');
  if(!email || !email.includes('@')){ msg.textContent='Enter your email first.'; return; }
  btn.disabled = true; msg.textContent = 'Creating payment...';
  try {
    const res = await fetch('/api/billing/checkout', {method:'POST',
      headers:{'content-type':'application/json'},
      body: JSON.stringify({email, tier, provider})});
    const data = await res.json();
    if(!res.ok) throw new Error(data.error || 'checkout failed');
    if(data.apiKey){ showKey(msg, data.apiKey); return; }
    location.href = data.url;
  } catch(e){ msg.textContent = e.message; btn.disabled = false; }
}
function showKey(el, key){
  el.innerHTML = 'Paid! Your API key: <code>'+key+'</code><br>Use it as ETUNNEL_TOKEN. Keep it safe.';
}
</script></body></html>`
	tpl := template.Must(template.New("p").Parse(pricingHTML))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = tpl.Execute(w, map[string]any{"Rows": rows, "Domain": s.cfg.Domain, "User": s.sessionEmail(r)})
}

func (s *Server) handleBillingCheckout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Email    string `json:"email"`
		Tier     string `json:"tier"`
		Provider string `json:"provider"` // "stripe" | "paypal"
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	tier, ok := s.cfg.Tiers[req.Tier]
	if !ok {
		http.Error(w, `{"error":"unknown tier"}`, http.StatusBadRequest)
		return
	}
	if _, buyable := s.tierPriceCents(req.Tier); !buyable {
		http.Error(w, `{"error":"tier not purchasable"}`, http.StatusBadRequest)
		return
	}
	if req.Email == "" || !strings.Contains(req.Email, "@") {
		http.Error(w, `{"error":"valid email required"}`, http.StatusBadRequest)
		return
	}
	if req.Provider != "stripe" && req.Provider != "paypal" && req.Provider != "crypto" {
		http.Error(w, `{"error":"provider must be stripe, paypal or crypto"}`, http.StatusBadRequest)
		return
	}
	// Subscribing requires a signed-in user; the session email wins so a
	// purchase can never be attributed to an address the buyer doesn't own.
	if session := s.sessionEmail(r); session != "" {
		req.Email = session
	} else {
		http.Error(w, `{"error":"login required before subscribing (see /login)"}`, http.StatusUnauthorized)
		return
	}
	cents, _ := s.tierPriceCents(req.Tier)
	base := s.billing.PublicBaseURL

	var (
		payURL string
		err    error
	)
	switch req.Provider {
	case "stripe":
		if s.billing.StripeSecretKey == "" {
			http.Error(w, `{"error":"card payments not configured"}`, http.StatusServiceUnavailable)
			return
		}
		payURL, err = s.stripeCheckout(base, req.Email, req.Tier, tier.Name, cents)
	case "paypal":
		if s.billing.PayPalClientID == "" {
			http.Error(w, `{"error":"paypal not configured"}`, http.StatusServiceUnavailable)
			return
		}
		payURL, err = s.paypalCreateOrder(base, req.Email, req.Tier, cents)
	case "crypto":
		if s.billing.NowPaymentsAPIKey == "" {
			http.Error(w, `{"error":"crypto payments not configured"}`, http.StatusServiceUnavailable)
			return
		}
		payURL, err = s.nowpaymentsInvoice(base, req.Email, req.Tier, cents)
	default:
		http.Error(w, `{"error":"provider must be stripe or paypal"}`, http.StatusBadRequest)
		return
	}
	if err != nil {
		s.logf("billing: %s checkout failed: %v", req.Provider, err)
		http.Error(w, `{"error":"payment provider error"}`, http.StatusBadGateway)
		return
	}
	_ = tier
	writeJSON(w, http.StatusOK, map[string]string{"url": payURL})
}

// handleBillingSuccess is the Stripe success redirect target; it resolves the
// session server-side and shows the API key.
func (s *Server) handleBillingSuccess(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("session_id")
	if sid == "" || s.billing.StripeSecretKey == "" {
		http.Redirect(w, r, "/pricing", http.StatusSeeOther)
		return
	}
	sess, err := s.stripeGetSession(sid)
	if err != nil || sess["payment_status"] != "paid" {
		s.logf("billing: success lookup failed: %v", err)
		http.Error(w, "could not confirm payment; if you were charged, the webhook will activate your account", http.StatusAccepted)
		return
	}
	email, _ := sess["customer_email"].(string)
	meta, _ := sess["metadata"].(map[string]any)
	tier, _ := meta["tier"].(string)
	key, err := s.activate(email, tier)
	if err != nil {
		http.Error(w, "activation failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeSuccessKeyPage(w, r, tier, email, key)
}

// handlePayPalReturn captures an approved order and activates.
func (s *Server) handlePayPalReturn(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token") // PayPal order id
	if token == "" || s.billing.PayPalClientID == "" {
		http.Redirect(w, r, "/pricing", http.StatusSeeOther)
		return
	}
	order, err := s.paypalCaptureOrder(token)
	if err != nil {
		s.logf("billing: paypal capture failed: %v", err)
		http.Error(w, "capture failed; if you were charged, the webhook will activate your account", http.StatusAccepted)
		return
	}
	email, tier := paypalOrderMeta(order)
	if email == "" || tier == "" {
		// capture responses sometimes omit purchase_units.custom_id;
		// GET order reliably returns it.
		s.logf("billing: paypal capture missing custom_id, falling back to GET order %s", token)
		if got, gerr := s.paypalReq("GET", "/v2/checkout/orders/"+url.PathEscape(token), nil); gerr == nil {
			email, tier = paypalOrderMeta(got)
		}
	}
	if !paypalCaptureCompleted(order) {
		s.logf("billing: paypal order %s captured but not cleared yet (eCheck?); webhook will activate", token)
		http.Error(w, "payment captured but still clearing (eCheck) — your account activates automatically once PayPal completes it", http.StatusAccepted)
		return
	}
	key, err := s.activate(email, tier)
	if err != nil {
		http.Error(w, "activation failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeSuccessKeyPage(w, r, tier, email, key)
}

// activate creates or renews the account for email; returns the API key.
func (s *Server) activate(email, tier string) (string, error) {
	if email == "" || tier == "" {
		return "", errors.New("missing email or tier in payment record")
	}
	if _, ok := s.cfg.Tiers[tier]; !ok {
		return "", fmt.Errorf("unknown tier %q", tier)
	}
	if acct, ok := s.accounts.LookupEmail(email); ok {
		// renewal/upgrade, key unchanged — write the tier under the store
		// mutex (#4: no lock-free writes to the shared *Account).
		s.accounts.Mutate(email, func(a *Account) { a.Tier = tier })
		_ = s.accounts.saveLockedSafe()
		s.notifyPayment(email, tier)
		return acct.APIKey, nil
	}
	acct, err := s.accounts.Create(email, tier)
	if err != nil {
		return "", err
	}
	s.notifyPayment(email, tier)
	return acct.APIKey, nil
}

// writeSuccessKeyPage renders the post-payment page. The freshly-activated
// API key is only shown to the logged-in buyer (#9) — success URL ids leak
// into browser history, Referer headers and proxy logs.
func (s *Server) writeSuccessKeyPage(w http.ResponseWriter, r *http.Request, tier, email, key string) {
	if se := s.sessionEmail(r); se == "" || se != email {
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><body style="font-family:system-ui">
<h2>Payment received 🎉</h2><p>Plan: <b>%s</b> for %s</p>
<p>Your API key is ready — log in at <a href="/me">/me</a> to view it.</p></body>`, tier, email)
		return
	}
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><body style="font-family:system-ui">
<h2>Payment received 🎉</h2><p>Plan: <b>%s</b> for %s</p>
<p>Your API key: <code>%s</code></p><p>Use it as <code>ETUNNEL_TOKEN</code>. Keep it safe.</p></body>`,
		tier, email, key)
}

// paypalCaptureCompleted reports whether a captured PayPal order is fully
// settled: order status COMPLETED and every capture resource COMPLETED
// (#5 — eCheck captures return 201 with PENDING captures that can bounce).
func paypalCaptureCompleted(order map[string]any) bool {
	if st, _ := order["status"].(string); st != "COMPLETED" {
		return false
	}
	units, _ := order["purchase_units"].([]any)
	for _, u := range units {
		unit, _ := u.(map[string]any)
		payments, _ := unit["payments"].(map[string]any)
		caps, _ := payments["captures"].([]any)
		for _, c := range caps {
			cm, _ := c.(map[string]any)
			if st, _ := cm["status"].(string); st != "" && st != "COMPLETED" {
				return false
			}
		}
	}
	return true
}

// nowpaymentsPaidEnough rejects clearly-underpaid payments (#3): the IPN
// carries the expected fiat price (price_amount) and the settled fiat amount
// (outcome_amount). Absent fields pass — the payment_status gate still applies.
func nowpaymentsPaidEnough(ev map[string]any) bool {
	price, okP := ev["price_amount"].(float64)
	paid, okA := ev["outcome_amount"].(float64)
	if !okP || !okA || price <= 0 {
		return true
	}
	return paid >= price*0.95
}

// --------------------------------------------------------------------------
// Stripe (REST, form-encoded)

var stripeHTTP = &http.Client{Timeout: 20 * time.Second}

func (s *Server) stripeReq(method, path string, form url.Values) (map[string]any, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, "https://api.stripe.com"+path, body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(s.billing.StripeSecretKey, "")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := stripeHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		if e, ok := out["error"].(map[string]any); ok {
			if msg, ok := e["message"].(string); ok {
				return nil, errors.New(msg)
			}
		}
		return nil, fmt.Errorf("stripe: %s", resp.Status)
	}
	return out, nil
}

func (s *Server) stripeCheckout(base, email, tier, tierName string, cents int64) (string, error) {
	form := url.Values{}
	form.Set("mode", "payment")
	form.Set("success_url", base+"/billing/success?session_id={CHECKOUT_SESSION_ID}")
	form.Set("cancel_url", base+"/pricing")
	form.Set("customer_email", email)
	form.Set("client_reference_id", email)
	form.Set("line_items[0][quantity]", "1")
	form.Set("line_items[0][price_data][currency]", "usd")
	form.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(cents, 10))
	form.Set("line_items[0][price_data][product_data][name]", "etunnel "+tierName+" (1 month)")
	form.Set("metadata[tier]", tier)
	form.Set("metadata[email]", email)
	out, err := s.stripeReq("POST", "/v1/checkout/sessions", form)
	if err != nil {
		return "", err
	}
	u, _ := out["url"].(string)
	if u == "" {
		return "", errors.New("stripe: no checkout url")
	}
	return u, nil
}

func (s *Server) stripeGetSession(id string) (map[string]any, error) {
	return s.stripeReq("GET", "/v1/checkout/sessions/"+url.PathEscape(id), nil)
}

// handleStripeWebhook verifies the v1 signature then activates on
// checkout.session.completed.
func (s *Server) handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	if s.billing.StripeWebhookSecret == "" {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	if !stripeVerifySignature(body, r.Header.Get("Stripe-Signature"), s.billing.StripeWebhookSecret, 5*time.Minute) {
		http.Error(w, "bad signature", http.StatusBadRequest)
		return
	}
	var ev struct {
		Type string `json:"type"`
		Data struct {
			Object struct {
				PaymentStatus string `json:"payment_status"`
				Metadata      struct {
					Email string `json:"email"`
					Tier  string `json:"tier"`
				} `json:"metadata"`
			} `json:"object"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &ev)
	if ev.Type == "checkout.session.completed" && ev.Data.Object.PaymentStatus == "paid" {
		if _, err := s.activate(ev.Data.Object.Metadata.Email, ev.Data.Object.Metadata.Tier); err != nil {
			s.logf("billing: stripe webhook activation failed: %v", err)
		}
	}
	w.WriteHeader(http.StatusOK)
}

// stripeVerifySignature checks "t=...,v1=..." HMAC over "t + . + body".
func stripeVerifySignature(body []byte, header, secret string, tolerance time.Duration) bool {
	var ts string
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sigs = append(sigs, v)
		}
	}
	if ts == "" || len(sigs) == 0 {
		return false
	}
	secs, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	if time.Since(time.Unix(secs, 0)) > tolerance || time.Unix(secs, 0).After(time.Now().Add(tolerance)) {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	want := mac.Sum(nil)
	for _, s := range sigs {
		got, err := hexDecode(s)
		if err != nil {
			continue
		}
		if subtle.ConstantTimeCompare(got, want) == 1 {
			return true
		}
	}
	return false
}

func hexDecode(s string) ([]byte, error) {
	return hex.DecodeString(s)
}

// --------------------------------------------------------------------------
// PayPal (Orders v2)

var paypalHTTP = &http.Client{Timeout: 20 * time.Second}

func (s *Server) paypalAPIBase() string {
	if strings.EqualFold(s.billing.PayPalEnv, "sandbox") {
		return "https://api-m.sandbox.paypal.com"
	}
	return "https://api-m.paypal.com"
}

var (
	ppTokMu   sync.Mutex
	ppTok     string
	ppTokExp  time.Time
	ppTokBase string
)

func (s *Server) paypalToken() (string, error) {
	ppTokMu.Lock()
	defer ppTokMu.Unlock()
	base := s.paypalAPIBase()
	if ppTok != "" && time.Now().Before(ppTokExp) && ppTokBase == base {
		return ppTok, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	req, _ := http.NewRequest("POST", base+"/v1/oauth2/token", strings.NewReader(form.Encode()))
	req.SetBasicAuth(s.billing.PayPalClientID, s.billing.PayPalSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := paypalHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.AccessToken == "" {
		return "", fmt.Errorf("paypal auth failed (%s)", resp.Status)
	}
	ppTok, ppTokBase = out.AccessToken, base
	ppTokExp = time.Now().Add(time.Duration(out.ExpiresIn-60) * time.Second)
	return ppTok, nil
}

func (s *Server) paypalReq(method, path string, payload any) (map[string]any, error) {
	var body io.Reader
	if payload != nil {
		b, _ := json.Marshal(payload)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.paypalAPIBase()+path, body)
	if err != nil {
		return nil, err
	}
	tok, err := s.paypalToken()
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := paypalHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		if desc, _ := out["message"].(string); desc != "" {
			return nil, errors.New(desc)
		}
		return nil, fmt.Errorf("paypal: %s", resp.Status)
	}
	return out, nil
}

func (s *Server) paypalCreateOrder(base, email, tier string, cents int64) (string, error) {
	order, err := s.paypalReq("POST", "/v2/checkout/orders", map[string]any{
		"intent": "CAPTURE",
		"purchase_units": []map[string]any{{
			"custom_id": email + ":" + tier,
			"amount":    map[string]any{"currency_code": "USD", "value": fmt.Sprintf("%d.%02d", cents/100, cents%100)},
		}},
		"application_context": map[string]any{
			"user_action": "PAY_NOW",
			"return_url":  base + "/billing/paypal/return",
			"cancel_url":  base + "/pricing",
			"brand_name":  "etunnel " + s.cfg.Domain,
		},
	})
	if err != nil {
		return "", err
	}
	links, _ := order["links"].([]any)
	for _, l := range links {
		m, _ := l.(map[string]any)
		if m["rel"] == "approve" {
			u, _ := m["href"].(string)
			return u, nil
		}
	}
	return "", errors.New("paypal: no approve link")
}

func (s *Server) paypalCaptureOrder(orderID string) (map[string]any, error) {
	return s.paypalReq("POST", "/v2/checkout/orders/"+url.PathEscape(orderID)+"/capture", map[string]any{})
}

func paypalOrderMeta(order map[string]any) (email, tier string) {
	units, _ := order["purchase_units"].([]any)
	if len(units) == 0 {
		return
	}
	m, _ := units[0].(map[string]any)
	cid, _ := m["custom_id"].(string)
	email, tier, _ = strings.Cut(cid, ":")
	return
}

// handlePayPalWebhook verifies via the API and activates on PAYMENT.CAPTURE.COMPLETED.
func (s *Server) handlePayPalWebhook(w http.ResponseWriter, r *http.Request) {
	if s.billing.PayPalWebhookID == "" {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	verify, err := s.paypalReq("POST", "/v1/notifications/verify-webhook-signature", map[string]any{
		"auth_algo":         r.Header.Get("PAYPAL-AUTH-ALGO"),
		"cert_url":          r.Header.Get("PAYPAL-CERT-URL"),
		"transmission_id":   r.Header.Get("PAYPAL-TRANSMISSION-ID"),
		"transmission_sig":  r.Header.Get("PAYPAL-TRANSMISSION-SIG"),
		"transmission_time": r.Header.Get("PAYPAL-TRANSMISSION-TIME"),
		"webhook_id":        s.billing.PayPalWebhookID,
		"webhook_event":     json.RawMessage(body),
	})
	if err != nil || verify["verification_status"] != "SUCCESS" {
		s.logf("billing: paypal webhook verification failed: %v", err)
		http.Error(w, "verification failed", http.StatusBadRequest)
		return
	}
	var ev struct {
		EventType string `json:"event_type"`
		Resource  struct {
			CustomID string `json:"custom_id"`
		} `json:"resource"`
	}
	_ = json.Unmarshal(body, &ev)
	if ev.EventType == "PAYMENT.CAPTURE.COMPLETED" {
		email, tier, _ := strings.Cut(ev.Resource.CustomID, ":")
		if _, err := s.activate(email, tier); err != nil {
			s.logf("billing: paypal webhook activation failed: %v", err)
		}
	}
	w.WriteHeader(http.StatusOK)
}

// saveLockedSafe persists the store from an HTTP handler context.
func (s *AccountStore) saveLockedSafe() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// --- NOWPayments (crypto) ---------------------------------------------------

var nowpayHTTP = &http.Client{Timeout: 20 * time.Second}

func (s *Server) nowpaymentsAPI(method, path string, payload any) (map[string]any, error) {
	var body io.Reader
	if payload != nil {
		b, _ := json.Marshal(payload)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "https://api.nowpayments.io"+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", s.billing.NowPaymentsAPIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := nowpayHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode >= 300 {
		if m, _ := out["message"].(string); m != "" {
			return nil, errors.New(m)
		}
		return nil, fmt.Errorf("nowpayments %s: %s", resp.Status, string(raw[:min(len(raw), 120)]))
	}
	return out, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (s *Server) nowpaymentsInvoice(base, email, tier string, cents int64) (string, error) {
	out, err := s.nowpaymentsAPI("POST", "/v1/invoice", map[string]any{
		"price_amount":     float64(cents) / 100,
		"price_currency":   "usd",
		"order_id":         email + ":" + tier,
		"success_url":      base + "/billing/crypto/success?payment=",
		"cancel_url":       base + "/pricing",
		"ipn_callback_url": base + "/webhooks/nowpayments",
	})
	if err != nil {
		return "", err
	}
	u, _ := out["invoice_url"].(string)
	if u == "" {
		return "", errors.New("nowpayments: no invoice_url")
	}
	return u, nil
}

// handleCryptoSuccess: NOWPayments appends the payment id to success_url;
// query it and activate on finished (settled) only.
func (s *Server) handleCryptoSuccess(w http.ResponseWriter, r *http.Request) {
	pid := strings.TrimPrefix(r.URL.Path, "/billing/crypto/success")
	pid = strings.Trim(pid, "/")
	if pid == "" && r.URL.Query().Get("payment") != "" {
		pid = r.URL.Query().Get("payment")
	}
	if pid == "" || s.billing.NowPaymentsAPIKey == "" {
		http.Redirect(w, r, "/pricing", http.StatusSeeOther)
		return
	}
	for i := 0; i < 3; i++ {
		// A departed client must not keep this handler (and its HTTP client)
		// busy for ~69s: the IPN activates the account anyway (#12).
		if r.Context().Err() != nil {
			return
		}
		st, err := s.nowpaymentsAPI("GET", "/v1/payment/"+url.PathEscape(pid), nil)
		if err == nil {
			status, _ := st["payment_status"].(string)
			if status == "finished" && nowpaymentsPaidEnough(st) { // #3: "confirmed" is seen-on-chain, not settled
				var email, tier string
				if oid, ok := st["order_id"].(string); ok {
					email, tier, _ = strings.Cut(oid, ":")
				}
				key, aerr := s.activate(email, tier)
				if aerr != nil {
					http.Error(w, "activation failed: "+aerr.Error(), http.StatusInternalServerError)
					return
				}
				s.writeSuccessKeyPage(w, r, tier, email, key)
				return
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
	http.Error(w, "payment not confirmed yet - it will activate automatically via IPN when the network confirms", http.StatusAccepted)
}

// handleNowPaymentsIPN verifies x-nowpayments-sig (HMAC-SHA512 over sorted
// key=value& pairs) and activates on finished (settled) only.
func (s *Server) handleNowPaymentsIPN(w http.ResponseWriter, r *http.Request) {
	if s.billing.NowPaymentsIPNSecret == "" {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var ev map[string]any
	if json.Unmarshal(body, &ev) != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
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
	mac := hmac.New(sha512.New, []byte(s.billing.NowPaymentsIPNSecret))
	mac.Write([]byte(sb.String()))
	want := hex.EncodeToString(mac.Sum(nil))
	got := r.Header.Get("x-nowpayments-sig")
	// sig may be "hex1,hex2" (sorted-string and base64 variants); accept any part
	ok := false
	for _, part := range strings.Split(got, ",") {
		if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(part)), []byte(want)) == 1 {
			ok = true
		}
	}
	if !ok {
		s.logf("billing: nowpayments IPN bad signature")
		http.Error(w, "bad signature", http.StatusBadRequest)
		return
	}
	status, _ := ev["payment_status"].(string)
	if status == "finished" && nowpaymentsPaidEnough(ev) { // #3: "confirmed" is seen-on-chain, not settled
		oid, _ := ev["order_id"].(string)
		email, tier, _ := strings.Cut(oid, ":")
		if _, err := s.activate(email, tier); err != nil {
			s.logf("billing: nowpayments activation failed: %v", err)
		}
	}
	w.WriteHeader(http.StatusOK)
}
