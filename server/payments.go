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
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
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

// subscriptionPeriod is how long one payment keeps the paid tier active.
const subscriptionPeriod = 30 * 24 * time.Hour

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

// cryptoPriceCents is the price charged on the crypto (NOWPayments) path.
// NOWPayments' minimum invoice size is dynamic per coin pair (network fees);
// low-fee coins floor around $2 but mainstream mainnets commonly sit at $3-5,
// which made the $3 Starter unpayable in practice. Crypto prices are set
// above that floor (and overridable without a rebuild via
// TIER_<ID>_CRYPTO_PRICE_CENTS); other providers keep tierPriceCents.
func (s *Server) cryptoPriceCents(tierID string) (int64, bool) {
	if v := os.Getenv("TIER_" + strings.ToUpper(tierID) + "_CRYPTO_PRICE_CENTS"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err == nil && n > 0 {
			return n, true
		}
	}
	builtin := map[string]int64{"starter": 500}
	if n, ok := builtin[tierID]; ok {
		return n, true
	}
	return s.tierPriceCents(tierID)
}

// --------------------------------------------------------------------------
// routes

func (s *Server) handlePricing(w http.ResponseWriter, r *http.Request) {
	type row struct {
		ID, Name, Price string
		TTLLine         string
		CustomLine      string
		MbpsLine        string
		CryptoLine      string
		Buyable         bool
	}
	var rows []row
	ids := make([]string, 0, len(s.cfg.Tiers))
	for id := range s.cfg.Tiers {
		ids = append(ids, id)
	}
	// Cheapest first: tier ids sort alphabetically (pro < starter), which would
	// put the most expensive plan first.
	sort.Slice(ids, func(i, j int) bool {
		pi, _ := s.tierPriceCents(ids[i])
		pj, _ := s.tierPriceCents(ids[j])
		if pi != pj {
			return pi < pj
		}
		return ids[i] < ids[j]
	})
	lang := pickLang(r)
	tr := func(key string) string { return T(lang, key) }
	user := s.sessionEmail(r)
	// Translation values carry trusted inline HTML (<b>, <a>) per the i18n
	// contract; the substituted {user} email is untrusted and stays escaped.
	// The finished line goes to the template as template.HTML so the wrapper
	// renders verbatim while html.EscapeString neutralises the email.
	subLine := tr("price.sub.anon")
	if user != "" {
		subLine = strings.ReplaceAll(strings.ReplaceAll(tr("price.sub.user"), "{user}", html.EscapeString(user)), "{account}", tr("foot.account"))
	}
	for _, id := range ids {
		t := s.cfg.Tiers[id]
		cents, buyable := s.tierPriceCents(id)
		p := ""
		if buyable {
			p = fmt.Sprintf("$%d.%02d/mo", cents/100, cents%100)
		}
		plural := ""
		if t.CustomDomains > 1 {
			plural = "s"
		}
		cryptoLine := ""
		if buyable {
			if cp, ok := s.cryptoPriceCents(id); ok && cp != cents {
				cryptoLine = fmt.Sprintf("$%d.%02d/mo", cp/100, cp%100)
			}
		}
		rows = append(rows, row{ID: id, Name: t.Name, Price: p,
			TTLLine:    strings.ReplaceAll(tr("price.li.ttl"), "{ttl}", humanTTL(t.TTL)),
			CustomLine: strings.ReplaceAll(strings.ReplaceAll(tr("price.li.custom"), "{n}", fmt.Sprintf("%d", t.CustomDomains)), "{s}", plural),
			MbpsLine:   strings.ReplaceAll(tr("price.li.mb"), "{n}", fmt.Sprintf("%d", t.Bandwidth/1_000_000)),
			CryptoLine: cryptoLine,
			Buyable:    buyable})
	}
	const pricingHTML = `<!doctype html><html lang="{{.Lang}}" dir="{{.Dir}}"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="icon" href="` + portalFavicon + `">
<title>{{T "price.title"}}</title>
<style>` + portalCSS + `
.wrap{max-width:1000px;margin:0 auto;padding:2.5rem 1.5rem}
.plans{display:grid;grid-template-columns:repeat(auto-fit,minmax(240px,1fr));gap:1rem;margin:1.2rem 0}
.plan .name{font-weight:700;font-size:1.05rem}
.plan .price{font-size:1.9rem;font-weight:800;margin:.4rem 0 .2rem}
.plan .price small{font-size:.8rem;color:var(--dim);font-weight:400}
.plan ul{list-style:none;margin:.7rem 0 1.1rem;padding:0;color:var(--dim);font-size:.85rem}
.plan li:before{content:"\2713 ";color:var(--green)}
.plan.hl{border-color:rgba(63,185,80,.5)}
.plan .pay{display:flex;gap:.5rem;flex-wrap:wrap}
.plan .pay .btn{flex:1 1 auto;text-align:center;padding:.55rem .8rem;font-size:.88rem}
.btn.paypal{background:#ffc439;color:#111}
@media(max-width:700px){.plans{grid-template-columns:1fr}}
</style></head><body>
<nav>
  <a class="logo" href="/" style="text-decoration:none">` + logoMark + ` <span>etunnel</span></a>
  <div class="links">
    <a href="/#quickstart">{{T "nav.quickstart"}}</a>
    <a href="/pricing">{{T "nav.pricing"}}</a>
    <a href="https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md">{{T "nav.docs"}}</a>
    {{if .User}}<a href="/logout">{{T "nav.signout"}}</a>{{else}}<a href="/login">{{T "nav.signin"}}</a>{{end}}
  </div>
</nav>
<main class="wrap">
<header>
  <h1>{{T "price.h1"}}</h1>
  <p class="sub">{{.SubLine}}</p>
</header>

{{if not .User}}
<div class="card">
  <p class="hint">{{T "price.hint"}}</p>
  <div class="row"><a class="btn" href="/login">{{T "price.signin"}}</a></div>
</div>
{{end}}

<div class="plans">
{{range .Rows}}{{if .Buyable}}
  <section class="card plan{{if eq .ID "pro"}} hl{{end}}">
    <div class="name">{{.Name}}</div>
    <div class="price">{{.Price}}</div>
    <ul>
      <li>{{.TTLLine}}</li>
      <li>{{.CustomLine}}</li>
      <li>{{.MbpsLine}}</li>
      <li>{{T "price.li.key"}}</li>
    </ul>
    <div class="pay">
      <button class="btn" onclick="pay(this,'{{.ID}}','crypto')">{{T "price.pay.crypto"}}{{if .CryptoLine}} &middot; {{.CryptoLine}}{{end}}</button>
      <button class="btn paypal" onclick="pay(this,'{{.ID}}','paypal')">{{T "price.pay.paypal"}}</button>
    </div>
  </section>
{{end}}{{end}}
</div>

<p class="msg" id="msg"></p>

<section class="card">
  <h2>{{T "price.nrp.h"}}</h2>
  <p class="hint">{{T "price.nrp.p"}}</p>
  <pre><code>npx @easytunnel/et-sdk 3000</code></pre>
</section>
</main>
<footer class="foot">{{.Domain}} &middot; <a href="/">{{T "foot.home"}}</a> &middot; <a href="https://github.com/topcheer/etunnel">github</a> &middot; <a href="/me">{{T "foot.account"}}</a> &middot; <a href="/terms">{{T "foot.terms"}}</a> &middot; <a href="/privacy">{{T "foot.privacy"}}</a>
<div class="langrow">{{.LangSwitch}}</div></footer>
<script>
async function pay(btn, tier, provider){
  const msg = document.getElementById('msg');
  btn.disabled = true; msg.className = 'msg'; msg.textContent = '{{.MsgCreating}}';
  try {
    const res = await fetch('/api/billing/checkout', {method:'POST',
      headers:{'content-type':'application/json'},
      body: JSON.stringify({tier, provider})});
    const data = await res.json();
    if(!res.ok) throw new Error(data.error || 'checkout failed');
    if(data.url){ location.href = data.url; return; }
    throw new Error('no payment url');
  } catch(e){
    msg.className = 'msg err';
    msg.textContent = (e.message === 'login required before subscribing (see /login)')
      ? '{{.MsgSigninFirst}}'
      : e.message;
    btn.disabled = false;
  }
}
</script>
</body></html>`

	tpl := template.Must(template.New("p").Funcs(template.FuncMap{
		"T": func(key string) template.HTML { return template.HTML(T(lang, key)) },
	}).Parse(pricingHTML))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = tpl.Execute(w, map[string]any{
		"Rows": rows, "Domain": s.cfg.Domain, "User": user,
		"SubLine": template.HTML(subLine), "Lang": lang, "Dir": langDir(lang),
		"LangSwitch":     template.HTML(langSwitchHTML(lang)),
		"MsgCreating":    tr("price.pay.creating"),
		"MsgSigninFirst": tr("price.pay.signinfirst"),
	})
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
	shortCents, isShort := shortDomainSKUCents(req.Tier, false)
	tier, isTier := s.cfg.Tiers[req.Tier]
	if !isTier && !isShort {
		http.Error(w, `{"error":"unknown tier"}`, http.StatusBadRequest)
		return
	}
	if isTier {
		if _, buyable := s.tierPriceCents(req.Tier); !buyable {
			http.Error(w, `{"error":"tier not purchasable"}`, http.StatusBadRequest)
			return
		}
	}
	// Email is optional in the request: the signed-in session supplies it.
	if req.Email != "" && !strings.Contains(req.Email, "@") {
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
	if isShort {
		cents = shortCents
	}
	productName := req.Tier
	if isTier {
		productName = tier.Name
	}
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
		payURL, err = s.stripeCheckout(base, req.Email, req.Tier, productName, cents)
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
		cryptoCents, _ := s.cryptoPriceCents(req.Tier)
		if isShort {
			cryptoCents, _ = shortDomainSKUCents(req.Tier, true)
		}
		payURL, err = s.nowpaymentsInvoice(base, req.Email, req.Tier, cryptoCents)
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
	// #40: the session id is a bearer capability - activation must only run
	// for the account that actually paid, never for whoever replays the id.
	if !s.paymentActingFor(r, email) {
		s.logf("billing: success refused: session %s belongs to %q, visitor %q", sid, email, s.sessionEmail(r))
		http.Error(w, "this payment belongs to a different account; log in as the buyer to claim it", http.StatusForbidden)
		return
	}
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
	// #40 (same family as /billing/success): the order token is a bearer
	// capability; only the buyer's session may capture-and-activate with it.
	if !s.paymentActingFor(r, email) {
		s.logf("billing: paypal return refused: order %s belongs to %q, visitor %q", token, email, s.sessionEmail(r))
		http.Error(w, "this payment belongs to a different account; log in as the buyer to claim it", http.StatusForbidden)
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
// Products with the "short-domain-" prefix are premium subdomain purchases
// and never touch the tier.
func (s *Server) activate(email, tier string) (string, error) {
	if email == "" || tier == "" {
		return "", errors.New("missing email or tier in payment record")
	}
	if strings.HasPrefix(tier, "short-domain-") {
		return s.activateShortDomain(email, tier)
	}
	if _, ok := s.cfg.Tiers[tier]; !ok {
		return "", fmt.Errorf("unknown tier %q", tier)
	}
	expires := time.Now().Add(subscriptionPeriod)
	if acct, ok := s.accounts.LookupEmail(email); ok {
		// renewal/upgrade, key unchanged — write the tier under the store
		// mutex (#4: no lock-free writes to the shared *Account). Both store
		// implementations persist inside Mutate; the JSON store additionally
		// needed the explicit save, which lives behind the jsonSaver cut.
		s.accounts.Mutate(email, func(a *Account) {
			a.Tier = tier
			a.ExpiresAt = expires // every successful payment renews the period
		})
		if js, ok := s.accounts.(jsonSaver); ok {
			_ = js.saveLockedSafe()
		}
		s.notifyPayment(email, tier)
		return acct.APIKey, nil
	}
	acct, err := s.accounts.Create(email, tier)
	if err != nil {
		return "", err
	}
	s.accounts.Mutate(email, func(a *Account) { a.ExpiresAt = expires })
	s.notifyPayment(email, tier)
	return acct.APIKey, nil
}

// activateShortDomain grants the premium short-subdomain entitlement for
// one subscription period. A Free account can buy one without a tier, so
// the account is created on demand.
func (s *Server) activateShortDomain(email, product string) (string, error) {
	n := 0
	for _, c := range strings.TrimPrefix(product, "short-domain-") {
		if c < '0' || c > '9' {
			n = 0
			break
		}
		n = n*10 + int(c-'0')
	}
	if n < 1 || n > 3 {
		return "", fmt.Errorf("invalid short-domain product %q", product)
	}
	expires := time.Now().Add(subscriptionPeriod)
	acct, ok := s.accounts.LookupEmail(email)
	if !ok {
		var err error
		acct, err = s.accounts.Create(email, "free")
		if err != nil {
			return "", err
		}
	}
	s.accounts.Mutate(email, func(a *Account) { a.PaidShort = &ShortDomain{Len: n, ExpiresAt: expires} })
	if js, ok := s.accounts.(jsonSaver); ok {
		_ = js.saveLockedSafe()
	}
	s.notifyPayment(email, product)
	return acct.APIKey, nil
}

// writeSuccessKeyPage renders the post-payment page. The freshly-activated
// API key is only shown to the logged-in buyer (#9) — success URL ids leak
// into browser history, Referer headers and proxy logs.
func (s *Server) writeSuccessKeyPage(w http.ResponseWriter, r *http.Request, tier, email, key string) {
	lang := pickLang(r)
	tr := func(k string) string { return T(lang, k) }
	planLine := strings.ReplaceAll(strings.ReplaceAll(tr("bill.plan"), "{plan}", tier), "{email}", email)
	if se := s.sessionEmail(r); se == "" || se != email {
		_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><body style="font-family:system-ui">
<h2>`+tr("bill.h1")+`</h2><p>`+planLine+`</p>
`+tr("bill.key.ready")+`</body>`)
		return
	}
	_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><body style="font-family:system-ui">
<h2>`+tr("bill.h1")+`</h2><p>`+planLine+`</p>
`+strings.ReplaceAll(tr("bill.key.is"), "{key}", key)+`</body>`)
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

// stripeAPIBase is a var so tests can point the client at a stub.
var stripeAPIBase = "https://api.stripe.com"

func (s *Server) stripeReq(method, path string, form url.Values) (map[string]any, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, stripeAPIBase+path, body)
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
// paymentEventDedup remembers payment events that already ran activate()
// so a captured-signature webhook replay inside the timestamp tolerance
// cannot re-run it (#39). Bounded: entries older than paymentEventWindow
// are dropped lazily on each call.
var (
	paymentEventsMu sync.Mutex
	paymentEvents   = map[string]time.Time{}
)

const paymentEventWindow = 7 * 24 * time.Hour

// paymentEventSeen marks id as processed and reports whether it was already
// seen. An empty id (provider sent none) never dedupes - behaviour then
// matches the pre-#39 code exactly.
func paymentEventSeen(id string) bool {
	if id == "" {
		return false
	}
	paymentEventsMu.Lock()
	defer paymentEventsMu.Unlock()
	now := time.Now()
	for k, ts := range paymentEvents {
		if now.Sub(ts) > paymentEventWindow {
			delete(paymentEvents, k)
		}
	}
	if _, ok := paymentEvents[id]; ok {
		return true
	}
	paymentEvents[id] = now
	return false
}

// paymentActingFor reports whether the visitor's login session may act for
// the buyer's email (#40): post-pay activation endpoints must only run for
// the account that paid, so a leaked session/order id cannot be replayed by
// someone else to trigger activation.
func (s *Server) paymentActingFor(r *http.Request, buyerEmail string) bool {
	se := s.sessionEmail(r)
	return se != "" && se == buyerEmail
}

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
		ID   string `json:"id"`
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
	if err := json.Unmarshal(body, &ev); err != nil {
		// a payload Stripe actually signed but we cannot parse must not
		// vanish behind a 200 (#94): surface it, let Stripe retry/diagnose
		s.logf("billing: stripe webhook payload parse failed: %v", err)
		http.Error(w, "{\"error\":\"bad payload\"}", http.StatusBadRequest)
		return
	}
	if ev.Type == "checkout.session.completed" && ev.Data.Object.PaymentStatus == "paid" {
		if paymentEventSeen("stripe:" + ev.ID) {
			s.logf("billing: stripe webhook replay suppressed (event %s)", ev.ID)
		} else if _, err := s.activate(ev.Data.Object.Metadata.Email, ev.Data.Object.Metadata.Tier); err != nil {
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
	if err := json.Unmarshal(body, &ev); err != nil {
		s.logf("billing: paypal webhook payload parse failed: %v", err)
		http.Error(w, "{\"error\":\"bad payload\"}", http.StatusBadRequest)
		return
	}
	if ev.EventType == "PAYMENT.CAPTURE.COMPLETED" {
		email, tier, _ := strings.Cut(ev.Resource.CustomID, ":")
		if _, err := s.activate(email, tier); err != nil {
			s.logf("billing: paypal webhook activation failed: %v", err)
		}
	}
	w.WriteHeader(http.StatusOK)
}

// jsonSaver is the extra surface only the JSON-file store needs.
type jsonSaver interface{ saveLockedSafe() error }

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
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("nowpayments %s: unparseable body: %w", resp.Status, err)
	}
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
		if paymentEventSeen("now:" + nowPaymentID(ev)) {
			s.logf("billing: nowpayments IPN replay suppressed (payment %s)", nowPaymentID(ev))
		} else if _, err := s.activate(email, tier); err != nil {
			s.logf("billing: nowpayments activation failed: %v", err)
		}
	}
	w.WriteHeader(http.StatusOK)
}

// nowPaymentID extracts the NOWPayments payment id as a string: the IPN
// sends it as a JSON number.
func nowPaymentID(ev map[string]any) string {
	switch v := ev["payment_id"].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

// startTierReaper downgrades paid accounts whose subscription lapsed.
// Runs hourly; the check is cheap (in-memory scan) and idempotent, so a
// restart simply re-evaluates the current state.
func (s *Server) startTierReaper(ctx context.Context) {
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.reapExpiredTiers(time.Now())
			}
		}
	}()
}

// reapExpiredTiers performs one pass and reports how many accounts lapsed.
func (s *Server) reapExpiredTiers(now time.Time) int {
	expired := s.accounts.ExpiredPaid(now)
	for _, email := range expired {
		if s.accounts.DowngradeToFree(email) {
			s.logf("billing: subscription expired, %s downgraded to free", email)
			if s.tg.enabled() {
				s.tgSend(fmt.Sprintf("⏳ %s subscription expired - downgraded to free", email))
			}
		}
	}
	return len(expired)
}

// humanTTL renders a tier lifetime compactly: 720h0m0s reads as "30d".
func humanTTL(d time.Duration) string {
	if d <= 0 {
		return "unlimited"
	}
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return d.String()
}
