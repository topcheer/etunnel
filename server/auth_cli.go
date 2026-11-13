// CLI loopback authorization flow (RFC 8252 style, the "gcloud auth login"
// pattern): the SDK listens on a random 127.0.0.1 port, the server hands out
// a short-lived authorization URL, the user approves in a browser (reusing
// the existing OAuth session cookie), the browser lands back on the loopback
// port with a one-time code, and the SDK exchanges it for its API key and
// custom-domain list.
//
// Two tokens, both HMAC-signed with the session key (so they survive a
// restart when SESSION_SECRET is set) and both short-lived:
//
//	device_code  start -> grant    payload "dev|nonce|redirect_uri|exp"  TTL 10m
//	code         grant -> exchange payload "code|email|nonce|exp"        TTL 5m, one-time
//
// The code carries the device nonce, and exchange requires both tokens to
// agree on it: a code minted for one CLI flow cannot be redeemed inside
// another, and a redeemed code cannot be replayed.
package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	cliDeviceTTL = 10 * time.Minute
	cliCodeTTL   = 5 * time.Minute
	// usedCodeTTL bounds how long a spent code stays in the one-time table:
	// well past its own 5-minute lifetime, so replay detection covers the
	// whole window in which the signature itself would still verify.
	usedCodeTTL = 15 * time.Minute
)

// cliUsedCodes remembers spent authorization codes (by nonce) for replay
// rejection. Lazily pruned on every take; the entries are tiny and bounded
// by the number of logins in 15 minutes.
type cliUsedCodes struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func (u *cliUsedCodes) take(nonce string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := time.Now()
	for k, exp := range u.seen {
		if now.After(exp) {
			delete(u.seen, k)
		}
	}
	if _, dup := u.seen[nonce]; dup {
		return false // replay
	}
	if u.seen == nil {
		u.seen = make(map[string]time.Time)
	}
	u.seen[nonce] = now.Add(usedCodeTTL)
	return true
}

// cliSign/verify build the compact "payload.signature" tokens used by the
// flow. Domain prefixes ("dev", "code") keep the two token kinds from being
// interchangeable even though they share one key.
func (s *Server) cliSign(domain, payload string) string {
	mac := hmacStr(s.sessionKey(), domain+"|"+payload)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + mac
}

func (s *Server) cliVerify(domain, tok string) (string, bool) {
	body, sig, ok := strings.Cut(tok, ".")
	if !ok || body == "" || sig == "" {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return "", false
	}
	if !hmacEqual(hmacStr(s.sessionKey(), domain+"|"+string(raw)), sig) {
		return "", false
	}
	return string(raw), true
}

// cliDevice is the payload of a device_code.
type cliDevice struct {
	Nonce       string `json:"n"`
	RedirectURI string `json:"r"`
	Exp         int64  `json:"e"`
}

// cliCode is the payload of an authorization code.
type cliCode struct {
	Email string `json:"m"`
	Nonce string `json:"n"`
	Exp   int64  `json:"e"`
}

func cliRand() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// validLoopbackRedirect enforces the RFC 8252 loopback shape
// http://127.0.0.1:<port>/callback (IPv6 [::1] accepted too). The port is
// ephemeral by design; the host must be loopback and the scheme plain http.
func validLoopbackRedirect(u string) bool {
	pu, err := url.Parse(u)
	if err != nil || pu.Scheme != "http" {
		return false
	}
	host := pu.Hostname()
	if host != "127.0.0.1" && host != "::1" {
		return false
	}
	_, port, err := net.SplitHostPort(pu.Host)
	if err != nil || port == "" || pu.Path != "/callback" || pu.RawQuery != "" || pu.Fragment != "" {
		return false
	}
	return true
}

// handleCLIStart mints the device_code for an SDK's loopback listener.
// POST /auth/cli/start  {"redirect_uri": "http://127.0.0.1:41234/callback"}
// -> {"auth_url": "...", "device_code": "..."}
func (s *Server) handleCLIStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		RedirectURI string `json:"redirect_uri"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if !validLoopbackRedirect(req.RedirectURI) {
		http.Error(w, `redirect_uri must be http://127.0.0.1:<port>/callback`, http.StatusBadRequest)
		return
	}
	dev := cliDevice{Nonce: cliRand(), RedirectURI: req.RedirectURI, Exp: time.Now().Add(cliDeviceTTL).Unix()}
	payload, _ := json.Marshal(dev)
	dc := s.cliSign("dev", string(payload))
	scheme := "https"
	if s.cfg.TLS.Mode == TLSModeHTTP {
		scheme = "http"
	}
	resp := map[string]any{
		"auth_url":    fmt.Sprintf("%s://%s/auth/cli/grant?dc=%s", scheme, s.cfg.Domain, url.QueryEscape(dc)),
		"device_code": dc,
		"expires_in":  int(cliDeviceTTL.Seconds()),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleCLIGrant serves the approval page (GET) and the approval itself
// (POST). It requires the browser session cookie - the same login that the
// web portal uses - so no new credential surface is introduced.
func (s *Server) handleCLIGrant(w http.ResponseWriter, r *http.Request) {
	if !s.canonicalAuthHost(w, r) {
		return
	}
	dc := r.FormValue("dc")
	payload, ok := s.cliVerify("dev", dc)
	if !ok {
		http.Error(w, "invalid or expired authorization request - run the CLI login again", http.StatusBadRequest)
		return
	}
	var dev cliDevice
	if err := json.Unmarshal([]byte(payload), &dev); err != nil || time.Now().Unix() > dev.Exp {
		http.Error(w, "invalid or expired authorization request - run the CLI login again", http.StatusBadRequest)
		return
	}
	email := s.sessionEmail(r)
	if email == "" {
		// Not logged in: send the user through the portal login and back.
		// The login handler stores return_to in a short-lived cookie and the
		// OAuth callback honours it, so this hop resumes at the consent page
		// automatically (the device code lives 10 minutes; the SDK's own wait
		// outlasts a normal login).
		http.Redirect(w, r, "/login?return_to="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Same design system as the portal (portalCSS + logoMark) so the
		// consent page reads as part of the site, not a bolted-on form.
		// Rendered via Replacer rather than Fprintf: the template embeds
		// portalCSS whose % bytes would corrupt a format string, and this
		// HTML-escapes the email for free (review issue #113).
		csrf := s.csrfMint(w)
		tpl := `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>CLI authorization - etunnel</title>
<link rel="icon" href="` + portalFavicon + `">
<style>` + portalCSS + `
body{display:flex;align-items:center;justify-content:center;min-height:100vh;padding:2rem 1rem;position:relative}
body::before{content:"";position:fixed;inset:0;pointer-events:none;background:radial-gradient(ellipse at 50% 0%,rgba(63,185,80,.13),transparent 55%),repeating-linear-gradient(90deg,transparent 0 79px,rgba(230,237,243,.03) 79px 80px)}
.auth{position:relative;width:100%;max-width:26rem}
.auth .card{padding:2.2rem 1.8rem;box-shadow:0 26px 70px rgba(0,0,0,.5)}
.brand{display:flex;align-items:center;gap:.65rem;font-weight:800;font-size:1.02rem;margin-bottom:1.4rem}
h1{font-size:1.3rem;font-weight:800;letter-spacing:-.02em;line-height:1.3;margin-bottom:.6rem}
.sub{color:var(--dim);font-size:.85rem;line-height:1.65;margin-bottom:1.5rem}
.sub code{background:rgba(230,237,243,.09);padding:.12rem .4rem;border-radius:5px;color:var(--fg)}
.actions{display:flex;align-items:center;gap:.8rem}
.foot{color:var(--dim);font-size:.72rem;text-align:center;margin-top:1.5rem;padding-top:1.2rem;border-top:1px solid var(--line)}
</style></head><body>
<div class="auth">
<div class="card">
<div class="brand">` + logoMark + ` etunnel</div>
<h1>Authorize command-line access?</h1>
<p class="sub">An application is asking for an API key for the account <code>{{EMAIL}}</code>.<br>It will be able to create tunnels and use this account's domains until you reset the key.</p>
<form method="post" class="actions"><input type="hidden" name="dc" value="{{DC}}"><input type="hidden" name="csrf" value="{{CSRF}}">
<button type="submit" name="action" value="allow" class="btn">Allow</button>
<a href="/" class="btn ghost">Cancel</a>
</form>
<div class="foot">Only approve if you started this login yourself.</div>
</div>
</div>
</body></html>`
		_, _ = w.Write([]byte(strings.NewReplacer("{{EMAIL}}", html.EscapeString(email), "{{DC}}", dc, "{{CSRF}}", csrf).Replace(tpl)))
		return
	}
	// CSRF: the Allow POST mints API keys, so it gets the full triple check
	// (#114). Attacker pages cannot read our cookie (HttpOnly, signed) nor
	// cross-site POST with a matching Origin.
	if !s.csrfValid(r, r.FormValue("csrf")) || !s.sameOrigin(r) {
		s.logf("auth: cli grant rejected for %s: csrf/origin check failed", r.RemoteAddr)
		http.Error(w, "authorization could not be verified - reload the page and try again", http.StatusForbidden)
		return
	}
	if r.FormValue("action") != "allow" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	code := cliCode{Email: email, Nonce: dev.Nonce, Exp: time.Now().Add(cliCodeTTL).Unix()}
	cp, _ := json.Marshal(code)
	signed := s.cliSign("code", string(cp))
	http.Redirect(w, r, dev.RedirectURI+"?code="+url.QueryEscape(signed), http.StatusSeeOther)
}

// handleCLIExchange redeems the one-time code (paired with the device_code)
// for the account's API key and domains.
// POST /auth/cli/exchange  {"device_code": "...", "code": "..."}
// -> {"api_token": "...", "subdomains": [...], "plan": "free|...", "expires_at": 0}
func (s *Server) handleCLIExchange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		DeviceCode string `json:"device_code"`
		Code       string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil || req.DeviceCode == "" || req.Code == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	devPayload, ok := s.cliVerify("dev", req.DeviceCode)
	if !ok {
		http.Error(w, "invalid or expired device code", http.StatusBadRequest)
		return
	}
	var dev cliDevice
	if err := json.Unmarshal([]byte(devPayload), &dev); err != nil || time.Now().Unix() > dev.Exp {
		http.Error(w, "invalid or expired device code", http.StatusBadRequest)
		return
	}
	codePayload, ok := s.cliVerify("code", req.Code)
	if !ok {
		http.Error(w, "invalid or expired code", http.StatusBadRequest)
		return
	}
	var code cliCode
	if err := json.Unmarshal([]byte(codePayload), &code); err != nil || time.Now().Unix() > code.Exp {
		http.Error(w, "invalid or expired code", http.StatusBadRequest)
		return
	}
	// Pairing: the code must belong to this device flow. Without this check
	// a code minted for flow A could be replayed against flow B's device
	// token within the 5-minute window.
	if code.Nonce != dev.Nonce {
		http.Error(w, "code does not match this authorization request", http.StatusBadRequest)
		return
	}
	// One-time use: claim the nonce atomically; a second exchange with the
	// same pair (or any other code reusing the nonce) is rejected.
	if !s.cliUsed.take(code.Nonce) {
		http.Error(w, "code already used", http.StatusBadRequest)
		return
	}
	acc, found := s.accounts.LookupEmail(code.Email)
	if !found {
		http.Error(w, "account not found", http.StatusUnauthorized)
		return
	}
	resp := map[string]any{
		"api_token":  acc.APIKey,
		"subdomains": acc.Domains,
		"plan":       acc.Tier,
		// API keys do not lapse on their own (the tier reaper downgrades the
		// account instead), so the SDK needs no refresh cycle.
		"expires_at": int64(0),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// --- CSRF (double-submit with a server-signed cookie) for #114 ---
//
// The consent form's Allow POST is protected by a triple layer:
//  1. et_pkcsrf cookie "nonce.signature" (HMAC with the session key - a
//     cookie minted by another origin cannot carry a valid signature);
//  2. the form's hidden csrf field must equal that nonce;
//  3. any Origin/Referer header present must be same-site (strict when
//     sent - modern browsers always send Origin on cross-site POSTs).
// No server-side ceremony state is required, so this works unchanged on a
// single node and on a mesh (the HMAC key is SESSION_SECRET).

const pkCsrfCookie = "et_pkcsrf"

func (s *Server) csrfMint(w http.ResponseWriter) string {
	nonce := cliRand()
	mac := hmacStr(s.sessionKey(), "csrf|"+nonce)
	http.SetCookie(w, &http.Cookie{Name: pkCsrfCookie, Value: nonce + "." + mac, Path: "/auth/cli/grant", MaxAge: 600, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.cfg.TLS.Mode != TLSModeHTTP})
	return nonce
}

// csrfValid checks the signed cookie against the submitted form token.
func (s *Server) csrfValid(r *http.Request, formToken string) bool {
	if formToken == "" {
		return false
	}
	c, err := r.Cookie(pkCsrfCookie)
	if err != nil {
		return false
	}
	nonce, mac, ok := strings.Cut(c.Value, ".")
	if !ok || nonce == "" || nonce != formToken {
		return false
	}
	return hmacEqual(hmacStr(s.sessionKey(), "csrf|"+nonce), mac)
}

// sameOrigin: when the browser supplies Origin or Referer it must be ours.
// Absent headers (very old clients) fall through to the token check.
func (s *Server) sameOrigin(r *http.Request) bool {
	for _, h := range []string{"Origin", "Referer"} {
		v := r.Header.Get(h)
		if v == "" {
			continue
		}
		u, err := url.Parse(v)
		if err != nil {
			return false
		}
		if !strings.EqualFold(u.Host, s.cfg.Domain) {
			return false
		}
	}
	return true
}
