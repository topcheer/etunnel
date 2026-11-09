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
	"time"
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
	type prov struct {
		ID, Label string
		Icon      template.HTML
	}
	var providers []prov
	for _, p := range []string{"google", "gitlab", "github", "apple"} {
		if s.oauthEnabled(p) {
			providers = append(providers, prov{ID: p, Label: providerLabel(p), Icon: providerIcon(p)})
		}
	}
	const loginHTML = `<!doctype html><meta charset="utf-8"><link rel="icon" href="data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 512 512' width='512' height='512' role='img' aria-label='etunnel'>  <rect width='512' height='512' rx='96' fill='#0d1117'/>  <rect x='120' y='128' width='272' height='256' rx='40' fill='none' stroke='#e6edf3' stroke-width='40'/>  <line x1='48' y1='256' x2='464' y2='256' stroke='#3fb950' stroke-width='40' stroke-linecap='round'/></svg>"><title>etunnel login</title>
<style>body{font-family:system-ui,sans-serif;background:#0b0e14;color:#e6edf3;display:flex;min-height:100vh;align-items:center;justify-content:center}main{text-align:center}a{display:flex;align-items:center;justify-content:center;gap:.6rem;margin:.6rem auto;padding:.7rem 1.2rem;width:15rem;border-radius:8px;text-decoration:none;font-weight:600;background:#21262d;color:#e6edf3}a:hover{background:#30363d}a svg{flex:none;width:20px;height:20px}</style>
<main><h1><svg width="40" height="40" viewBox="0 0 512 512" aria-label="etunnel" style="vertical-align:middle;margin-right:.6rem"><rect width="512" height="512" rx="96" fill="#0d1117"/><rect x="120" y="128" width="272" height="256" rx="40" fill="none" stroke="#e6edf3" stroke-width="40"/><line x1="48" y1="256" x2="464" y2="256" stroke="#3fb950" stroke-width="40" stroke-linecap="round"/></svg>Sign in to etunnel</h1><p>Required before subscribing.</p>
{{range .Providers}}<a href="/auth/{{.ID}}/login">{{.Icon}}<span>{{.Label}}</span></a>{{end}}
</main>`
	tpl := template.Must(template.New("l").Parse(loginHTML))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = tpl.Execute(w, map[string]any{"Providers": providers})
}

func providerLabel(p string) string {
	switch p {
	case "google":
		return "Google"
	case "gitlab":
		return "GitLab"
	case "github":
		return "GitHub"
	case "apple":
		return "Apple"
	}
	return p
}

// providerIcon returns the inline brand mark (official simplified paths).
func providerIcon(p string) template.HTML {
	switch p {
	case "google":
		return template.HTML(`<svg viewBox="0 0 48 48"><path fill="#EA4335" d="M24 9.5c3.54 0 6.71 1.22 9.21 3.6l6.85-6.85C35.9 2.38 30.47 0 24 0 14.62 0 6.51 5.38 2.56 13.22l7.98 6.19C12.43 13.72 17.74 9.5 24 9.5z"/><path fill="#4285F4" d="M46.98 24.55c0-1.57-.15-3.09-.38-4.55H24v9.02h12.94c-.58 2.96-2.26 5.48-4.78 7.18l7.73 6c4.51-4.18 7.09-10.36 7.09-17.65z"/><path fill="#FBBC05" d="M10.53 28.59c-.48-1.45-.76-2.99-.76-4.59s.27-3.14.76-4.59l-7.98-6.19C.92 16.46 0 20.12 0 24c0 3.88.92 7.54 2.56 10.78l7.97-6.19z"/><path fill="#34A853" d="M24 48c6.48 0 11.93-2.13 15.89-5.81l-7.73-6c-2.15 1.45-4.92 2.3-8.16 2.3-6.26 0-11.57-4.22-13.47-9.91l-7.98 6.19C6.51 42.62 14.62 48 24 48z"/></svg>`)
	case "gitlab":
		return template.HTML(`<svg viewBox="0 0 48 48"><path fill="#E24329" d="M24 44 33.1 16h-18.2L24 44z"/><path fill="#FC6D26" d="M24 44 14.9 16H4.5L24 44z"/><path fill="#FCA326" d="M4.5 16 .9 27c-.3 1.1.03 2.3.9 3L24 44 4.5 16z"/><path fill="#FC6D26" d="M24 44 33.1 16h10.4L24 44z"/><path fill="#FCA326" d="M43.5 16 47.1 27c.3 1.1-.03 2.3-.9 3L24 44 43.5 16z"/><path fill="#E24329" d="M4.5 16H14.9l-4.1-12c-.2-.6-1-.6-1.2 0L4.5 16z"/><path fill="#E24329" d="M43.5 16H33.1l4.1-12c.2-.6 1-.6 1.2 0L43.5 16z"/></svg>`)
	case "github":
		return template.HTML(`<svg viewBox="0 0 16 16" fill="#e6edf3"><path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82a7.5 7.5 0 0 1 2-.27c.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8z"/></svg>`)
	case "apple":
		return template.HTML(`<svg viewBox="0 0 24 24" fill="#e6edf3"><path d="M17.05 12.54c-.02-2.36 1.93-3.5 2.02-3.55-1.1-1.61-2.82-1.83-3.43-1.85-1.46-.15-2.85.86-3.59.86-.74 0-1.88-.84-3.09-.82-1.59.02-3.06.92-3.88 2.35-1.65 2.87-.42 7.11 1.18 9.44.79 1.14 1.73 2.42 2.96 2.37 1.19-.05 1.64-.77 3.08-.77 1.44 0 1.84.77 3.09.75 1.28-.02 2.09-1.16 2.87-2.31.9-1.32 1.27-2.6 1.29-2.66-.03-.01-2.48-.95-2.5-3.81zM14.62 5.6c.65-.79 1.09-1.89.97-2.98-.94.04-2.08.63-2.75 1.41-.6.7-1.13 1.82-.99 2.89 1.05.08 2.12-.53 2.77-1.32z"/></svg>`)
	}
	return ""
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
<p>Plan: <b>{{.Tier}}</b>{{if .MaxDomains}} (up to {{.MaxDomains}} custom subdomains){{end}}{{if .Expires}}<br><span style="color:#8b949e">renews/expires: {{.Expires}}</span>{{end}}</p>
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
		"Expires":    expiresLabel(acct.ExpiresAt),
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

// expiresLabel renders the subscription end date (empty for no-expiry).
func expiresLabel(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	left := time.Until(t)
	if left < 0 {
		return t.Format("2006-01-02") + " (lapsed)"
	}
	return fmt.Sprintf("%s (%d days left)", t.Format("2006-01-02"), int(left.Hours()/24))
}
