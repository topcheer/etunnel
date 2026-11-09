package server

// User portal: /login (provider picker), /me (account dashboard) and the
// authenticated actions - subscribe/change plan (via /pricing), cancel,
// reset API key, custom domains, close account.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func hmacStr(key []byte, msg string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return fmt.Sprintf("%x", m.Sum(nil))
}

func hmacEqual(a, b string) bool {
	return a != "" && hmac.Equal([]byte(a), []byte(b))
}

func parseInt64(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var providers []string
	for _, p := range []string{"google", "gitlab", "github", "apple"} {
		if s.oauthEnabled(p) {
			providers = append(providers, p)
		}
	}
	const loginHTML = `<!doctype html><meta charset="utf-8"><link rel="icon" href="data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 512 512' width='512' height='512' role='img' aria-label='etunnel'>  <rect width='512' height='512' rx='96' fill='#0d1117'/>  <rect x='120' y='128' width='272' height='256' rx='40' fill='none' stroke='#e6edf3' stroke-width='40'/>  <line x1='48' y1='256' x2='464' y2='256' stroke='#3fb950' stroke-width='40' stroke-linecap='round'/></svg>"><title>etunnel login</title>
<style>body{font-family:system-ui,sans-serif;background:#0b0e14;color:#e6edf3;display:flex;min-height:100vh;align-items:center;justify-content:center}main{text-align:center}a{display:block;margin:.6rem auto;padding:.7rem 1.2rem;width:14rem;border-radius:8px;text-decoration:none;font-weight:600}</style>
<main><h1>Sign in to etunnel</h1><p>Required before subscribing.</p>
{{range .Providers}}<a href="/auth/{{.}}/login" style="background:#21262d;color:#e6edf3">{{.}}</a>{{end}}
</main>`
	tpl := template.Must(template.New("l").Parse(loginHTML))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = tpl.Execute(w, map[string]any{"Providers": providers})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "et_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/pricing", http.StatusSeeOther)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	email := s.sessionEmail(r)
	if email == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	acct, _ := s.accounts.LookupEmail(email)
	tier := s.cfg.Tiers[acct.Tier]
	maxDomains := 0
	if tier != nil {
		maxDomains = tier.CustomDomains
	}
	domains := strings.Join(acct.Domains, ", ")

	const meHTML = `<!doctype html><meta charset="utf-8"><title>etunnel - my account</title>
<style>body{font-family:system-ui,sans-serif;background:#0b0e14;color:#e6edf3;max-width:640px;margin:2rem auto;padding:0 1rem}
button{background:#238636;color:#fff;border:0;border-radius:6px;padding:.5rem 1rem;margin:.3rem;cursor:pointer}
.danger{background:#da3633}code{background:#161b22;padding:.2rem .4rem;border-radius:4px}
h2{border-bottom:1px solid #21262d;padding-bottom:.4rem}</style>
<h2>Signed in as {{.Email}}</h2>
<p style="color:#8b949e">via {{.Provider}}{{if .Name}} &middot; {{.Name}}{{end}} &middot; <a href="/logout" style="color:#58a6ff">sign out</a></p>
<p>Plan: <b>{{.Tier}}</b>{{if .MaxDomains}} (up to {{.MaxDomains}} custom subdomains){{end}}</p>
<p>API key: <code>{{.Key}}</code> <button onclick="act('reset')">Reset key</button></p>
<h3>Subscription</h3>
<p><a href="/pricing" style="color:#58a6ff">Subscribe / change plan</a>
<button class="danger" onclick="act('cancel')">Cancel subscription</button></p>
<h3>Custom subdomains</h3>
<p>Owned: <code>{{.Domains}}</code></p>
<p><input id="dom" placeholder="my-sub"> <button onclick="addDom()">Add</button>
<button class="danger" onclick="delDom()">Remove</button></p>
<h3>Danger zone</h3>
<button class="danger" onclick="if(confirm('Delete account permanently?'))act('close')">Close account</button>
<p id="msg"></p>
<script>
const email = {{.EmailJS}};
async function act(a, body){
  const res = await fetch('/me/'+a, {method:'POST',headers:{'content-type':'application/json'},
    body: JSON.stringify(body||{email})});
  const data = await res.json().catch(()=>({}));
  document.getElementById('msg').textContent = res.ok ? (data.message||data.apiKey||'done') : (data.error||'failed');
  if(res.ok && a!=='cancel') setTimeout(()=>location.reload(), 800);
  if(res.ok && (a==='cancel'||a==='close')) setTimeout(()=>location.reload(), 800);
}
function addDom(){ act('domain', {email, domain: document.getElementById('dom').value, remove:false}); }
function delDom(){ act('domain', {email, domain: document.getElementById('dom').value, remove:true}); }
</script>`
	tpl := template.Must(template.New("m").Parse(meHTML))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = tpl.Execute(w, map[string]any{
		"Email":      acct.Email,
		"Provider":   acct.Provider,
		"Name":       acct.Name,
		"EmailJS":    template.JS(fmt.Sprintf("%q", acct.Email)),
		"Tier":       acct.Tier,
		"Key":        acct.APIKey,
		"MaxDomains": maxDomains,
		"Domains":    domains,
	})
}

func (s *Server) handleMeAction(w http.ResponseWriter, r *http.Request, action string) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	session := s.sessionEmail(r)
	if session == "" {
		http.Error(w, `{"error":"login required"}`, http.StatusUnauthorized)
		return
	}
	var req struct {
		Email, Domain string
		Remove        bool
	}
	_ = jsonDecodeBody(r, &req)
	// Authorization: only the session owner may act on their account.
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email != session {
		http.Error(w, `{"error":"cannot act on another account"}`, http.StatusForbidden)
		return
	}
	acct, ok := s.accounts.LookupEmail(email)
	if !ok {
		http.Error(w, `{"error":"no account"}`, http.StatusNotFound)
		return
	}
	switch action {
	case "reset":
		key, ok := s.accounts.ResetKey(email)
		if !ok {
			http.Error(w, `{"error":"reset failed"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"apiKey": key, "message": "API key reset (old key revoked)"})
	case "cancel":
		if acct.Tier == "free" {
			writeJSON(w, http.StatusOK, map[string]string{"message": "already on free"})
			return
		}
		s.accounts.CancelSubscription(email)
		writeJSON(w, http.StatusOK, map[string]string{"message": "subscription cancelled - back on free tier"})
	case "domain":
		dom := strings.ToLower(strings.TrimSpace(req.Domain))
		if dom == "" || !validSubdomain(dom) {
			http.Error(w, `{"error":"invalid subdomain"}`, http.StatusBadRequest)
			return
		}
		tier := s.cfg.Tiers[acct.Tier]
		if tier == nil {
			http.Error(w, `{"error":"unknown tier"}`, http.StatusInternalServerError)
			return
		}
		if req.Remove {
			s.accounts.Mutate(email, func(a *Account) { a.Domains = removeStr(a.Domains, dom) })
			writeJSON(w, http.StatusOK, map[string]string{"message": "removed " + dom})
			return
		}
		owned := false
		_ = s.accounts.Mutate(email, func(a *Account) {
			if containsStr(a.Domains, dom) {
				owned = true
				return
			}
			if len(a.Domains) >= tier.CustomDomains {
				return
			}
			a.Domains = append(a.Domains, dom)
			owned = true
		})
		if !owned {
			http.Error(w, `{"error":"tier domain limit reached (upgrade for more)"}`, http.StatusForbidden)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"message": "domain " + dom + " added"})
	case "close":
		// Only allow closing accounts with no active tunnels.
		for _, sess := range s.reg.Snapshot() {
			if sess.account != nil && sess.account.Email == email {
				http.Error(w, `{"error":"close your tunnels first (/tunnels admin or stop clients)"}`, http.StatusConflict)
				return
			}
		}
		s.accounts.Delete(acct.APIKey)
		http.SetCookie(w, &http.Cookie{Name: "et_session", Value: "", Path: "/", MaxAge: -1})
		writeJSON(w, http.StatusOK, map[string]string{"message": "account closed"})
	default:
		http.Error(w, `{"error":"unknown action"}`, http.StatusNotFound)
	}
}

func jsonDecodeBody(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(v)
}

func validSubdomain(s string) bool {
	if len(s) < 3 || len(s) > 63 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return s[0] != '-' && s[len(s)-1] != '-'
}

func containsStr(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func removeStr(xs []string, v string) []string {
	out := xs[:0]
	for _, x := range xs {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}
