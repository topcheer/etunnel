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

// portalFavicon, portalCSS and logoMark are the design system shared by the
// account pages. They mirror the landing page (same palette, mono type, 12px
// cards, green accent) so signing in does not feel like leaving the site.
const portalFavicon = `data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 512 512' width='512' height='512' role='img' aria-label='etunnel'>  <rect width='512' height='512' rx='96' fill='%230d1117'/>  <rect x='120' y='128' width='272' height='256' rx='40' fill='none' stroke='%23e6edf3' stroke-width='40'/>  <line x1='48' y1='256' x2='464' y2='256' stroke='%233fb950' stroke-width='40' stroke-linecap='round'/></svg>`

const logoMark = `<svg width="26" height="26" viewBox="0 0 512 512" aria-hidden="true"><rect width="512" height="512" rx="96" fill="#0d1117"/><rect x="120" y="128" width="272" height="256" rx="40" fill="none" stroke="#e6edf3" stroke-width="40"/><line x1="48" y1="256" x2="464" y2="256" stroke="#3fb950" stroke-width="40" stroke-linecap="round"/></svg>`

const portalCSS = `
:root{--bg:#0b0e14;--panel:#0d1117;--line:#21262d;--fg:#e6edf3;--dim:#8b949e;--green:#3fb950;--green2:#7ee787;--blue:#58a6ff;--red:#da3633}
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:ui-monospace,'SF Mono',Menlo,Consolas,monospace;background:var(--bg);color:var(--fg);line-height:1.6;-webkit-font-smoothing:antialiased}
a{color:var(--blue);text-decoration:none}a:hover{text-decoration:underline}
input,button{font-family:inherit;font-size:.85rem}
input{background:#0b0e14;border:1px solid var(--line);border-radius:8px;color:var(--fg);padding:.5rem .7rem;outline:none}
input:focus{border-color:rgba(63,185,80,.55)}
code{background:#161b22;border:1px solid var(--line);border-radius:6px;padding:.1rem .4rem;font-size:.78rem}
nav{display:flex;align-items:center;justify-content:space-between;gap:1rem;padding:1rem 2rem;border-bottom:1px solid var(--line);background:rgba(11,14,20,.85);backdrop-filter:blur(8px);position:sticky;top:0;z-index:10}
nav .logo{display:flex;align-items:center;gap:.6rem;font-weight:700;font-size:1.05rem;color:var(--fg)}
nav .links{display:flex;gap:1.5rem;font-size:.85rem}
nav .links a{color:var(--dim)}nav .links a:hover{color:var(--fg);text-decoration:none}
.card{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:1.5rem}
.card h2{font-size:.95rem;font-weight:700;letter-spacing:-.01em;margin-bottom:.25rem}
.hint{color:var(--dim);font-size:.76rem}
.btn{display:inline-flex;align-items:center;justify-content:center;gap:.45rem;border-radius:8px;padding:.55rem 1.05rem;font-size:.82rem;font-weight:700;border:1px solid transparent;cursor:pointer;background:var(--green);color:#0b0e14;text-decoration:none}
.btn:hover{background:var(--green2);text-decoration:none}
.btn.ghost{background:rgba(13,17,23,.6);border-color:var(--line);color:var(--fg)}
.btn.ghost:hover{border-color:var(--dim);background:var(--panel)}
.btn.danger{background:transparent;border-color:rgba(218,54,51,.6);color:#ff7b72}
.btn.danger:hover{background:rgba(218,54,51,.14);border-color:var(--red)}
.badge{display:inline-block;max-width:100%;overflow:hidden;text-overflow:ellipsis;border:1px solid var(--line);border-radius:99px;padding:.15rem .7rem;font-size:.72rem;color:var(--dim);white-space:nowrap}
.g{color:var(--green2)}
.row{display:flex;gap:.5rem;flex-wrap:wrap;align-items:center}
@media(max-width:700px){
  nav{flex-wrap:wrap;justify-content:center;gap:.45rem;padding:.75rem 1rem}
  nav .links{order:3;width:100%;justify-content:center;gap:1.1rem;font-size:.8rem}
  .card{padding:1.25rem}
}`

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
	loginHTML := `<!doctype html>
<html lang="{{.Lang}}" dir="{{.Dir}}"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>{{T "login.title"}}</title>
<link rel="icon" href="` + portalFavicon + `">
<style>` + portalCSS + `
body{display:flex;align-items:center;justify-content:center;min-height:100vh;padding:2rem 1rem;position:relative}
body::before{content:"";position:fixed;inset:0;pointer-events:none;background:radial-gradient(ellipse at 50% 0%,rgba(63,185,80,.13),transparent 55%),repeating-linear-gradient(90deg,transparent 0 79px,rgba(230,237,243,.03) 79px 80px)}
.auth{position:relative;width:100%;max-width:26rem}
.auth .card{padding:2.2rem 1.8rem;box-shadow:0 26px 70px rgba(0,0,0,.5)}
.brand{display:flex;align-items:center;gap:.65rem;font-weight:800;font-size:1.02rem;margin-bottom:1.5rem}
h1{font-size:1.45rem;font-weight:800;letter-spacing:-.02em;line-height:1.25;margin-bottom:.45rem}
.sub{color:var(--dim);font-size:.84rem;margin-bottom:1.5rem}
.prov{display:flex;align-items:center;gap:.75rem;width:100%;padding:.72rem 1rem;margin-bottom:.55rem;border:1px solid var(--line);border-radius:9px;background:rgba(22,27,34,.55);color:var(--fg);font-weight:600;font-size:.86rem;text-decoration:none}
.prov:hover{border-color:rgba(63,185,80,.55);background:#161b22;text-decoration:none}
.prov svg{flex:none;width:19px;height:19px}
.prov .arrow{margin-left:auto;color:var(--dim);font-weight:400}
.none{border:1px dashed var(--line);border-radius:9px;padding:1rem;text-align:center;color:var(--dim);font-size:.82rem}
.fine{color:var(--dim);font-size:.71rem;line-height:1.55;margin-top:1.4rem;padding-top:1.1rem;border-top:1px solid var(--line)}
.foot{display:flex;justify-content:center;gap:.7rem;font-size:.77rem;color:var(--dim);margin-top:1.4rem}
.foot a{color:var(--dim)}.foot a:hover{color:var(--fg)}
@media(max-width:700px){body{padding:1.25rem .9rem}.auth .card{padding:1.7rem 1.25rem}}
</style></head><body>
<div class="auth">
<div class="card">
<div class="brand">` + logoMark + ` etunnel</div>
<h1>{{T "login.h1.pre"}}<span class="g">{{T "login.h1.em"}}</span></h1>
<p class="sub">{{T "login.sub"}}</p>
{{if .Providers}}{{range .Providers}}<a class="prov" href="/auth/{{.ID}}/login">{{.Icon}}<span>{{T "login.continue"}} {{.Label}}</span><span class="arrow">&#8594;</span></a>{{end}}{{else}}<p class="none">{{T "login.none"}}</p>{{end}}
<p class="fine">{{T "login.fine"}}</p>
</div>
<div class="foot"><a href="/">&#8592; {{T "foot.home"}} etunnel</a><span>&middot;</span><a href="/pricing">{{T "nav.pricing"}}</a><span>&middot;</span><a href="https://github.com/topcheer/etunnel">{{T "nav.docs"}}</a><span>&middot;</span><a href="/terms">{{T "foot.terms"}}</a><span>&middot;</span><a href="/privacy">{{T "foot.privacy"}}</a></div>
</div>
<div class="langrow">{{.LangSwitch}}</div>
</body></html>`

	lang := pickLang(r)
	tpl := template.Must(template.New("l").Funcs(template.FuncMap{
		"T": func(key string) string { return T(lang, key) },
	}).Parse(loginHTML))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = tpl.Execute(w, map[string]any{"Providers": providers, "Lang": lang, "Dir": langDir(lang), "LangSwitch": template.HTML(langSwitchHTML(lang))})
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

	meHTML := `<!doctype html>
<html lang="{{.Lang}}" dir="{{.Dir}}"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>{{T "me.title"}}</title>
<link rel="icon" href="` + portalFavicon + `">
<style>` + portalCSS + `
body{padding-bottom:4rem}
.wrap{max-width:1000px;margin:0 auto;padding:2.5rem 1.5rem}
.pagehead{margin-bottom:1.8rem}
.pagehead h1{font-size:1.55rem;font-weight:800;letter-spacing:-.02em}
.pagehead .sub{color:var(--dim);font-size:.84rem;margin-top:.3rem}
.grid{display:grid;grid-template-columns:1fr 1fr;gap:1rem;align-items:start}
.wide{grid-column:1/-1}
.tier{font-size:1.3rem;font-weight:800;line-height:1.2;color:var(--green2);text-transform:capitalize}
.key{display:flex;gap:.5rem;flex-wrap:wrap}
.key input{flex:1 1 12rem;min-width:0;font-size:.76rem;color:var(--green2);letter-spacing:.02em}
.chips{display:flex;flex-wrap:wrap;gap:.4rem;margin-bottom:1rem;min-height:1.6rem;align-items:center}
.chip{border:1px solid var(--line);border-radius:99px;padding:.18rem .65rem;font-size:.76rem;background:rgba(22,27,34,.6)}
.empty{color:var(--dim);font-size:.78rem}
.zone{border-color:rgba(218,54,51,.45)}
.msg{margin-top:1.2rem;font-size:.82rem;min-height:1.3rem;color:var(--dim)}
.msg.ok{color:var(--green2)}.msg.err{color:#ff7b72}
@media(max-width:700px){.wrap{padding:1.7rem 1rem}.pagehead h1{font-size:1.35rem}.grid{grid-template-columns:1fr}}
</style></head><body>
<nav>
  <a class="logo" href="/">` + logoMark + ` etunnel</a>
  <div class="links"><a href="/#quickstart">{{T "nav.quickstart"}}</a><a href="/pricing">{{T "nav.pricing"}}</a><a href="https://github.com/topcheer/etunnel">{{T "nav.docs"}}</a><a href="/logout">{{T "nav.signout"}}</a></div>
</nav>
<main class="wrap">
<header class="pagehead">
  <h1>{{T "me.h1"}}</h1>
  <p class="sub">{{.SubLine}}</p>
</header>
<div class="grid">
  <section class="card">
    <h2>{{T "me.plan.h"}}</h2>
    <p class="hint">{{T "me.plan.hint"}}</p>
    <div class="row" style="margin:.8rem 0 .35rem"><span class="tier">{{.Tier}}</span></div>
    {{if .Expires}}<div class="row" style="margin-bottom:.9rem"><span class="badge">{{.ExpiresLine}}</span></div>{{end}}
    <p class="hint" style="margin-bottom:1.1rem">{{.MaxDomainsLine}}</p>
    <div class="row"><a class="btn" href="/pricing">{{T "me.plan.sub"}}</a><button class="btn ghost" onclick="act('cancel')">{{T "me.plan.cancel"}}</button></div>
  </section>
  <section class="card">
    <h2>{{T "me.key.h"}}</h2>
    <p class="hint" style="margin-bottom:.9rem">{{T "me.key.hint"}}</p>
    <div class="key"><input id="apikey" value="{{.Key}}" readonly><button class="btn ghost" onclick="copyKey(this)">{{T "me.key.copy"}}</button><button class="btn ghost" onclick="act('reset')">{{T "me.key.reset"}}</button></div>
  </section>
  <section class="card wide">
    <h2>{{T "me.dom.h"}}</h2>
    <p class="hint" style="margin-bottom:.9rem">{{T "me.dom.hint"}}</p>
    <div class="chips">{{range .DomainList}}<span class="chip">{{.}}</span>{{else}}<span class="empty">{{T "me.dom.none"}}</span>{{end}}</div>
    <div class="row"><input id="dom" placeholder="my-sub" style="flex:1 1 12rem"><button class="btn" onclick="addDom()">{{T "me.dom.add"}}</button><button class="btn ghost" onclick="delDom()">{{T "me.dom.remove"}}</button></div>
  </section>
  <section class="card wide zone">
    <h2>{{T "me.zone.h"}}</h2>
    <p class="hint" style="margin-bottom:1rem">{{T "me.zone.hint"}}</p>
    <button class="btn danger" onclick="if(confirm('{{.ZoneConfirm}}'))act('close')">{{T "me.zone.close"}}</button>
  </section>
</div>
<p id="msg" class="msg"></p>
</main>
<script>
const email = {{.EmailJS}};
async function act(a, body){
  const res = await fetch('/me/'+a, {method:'POST',headers:{'content-type':'application/json'},
    body: JSON.stringify(body||{email})});
  const data = await res.json().catch(()=>({}));
  const el = document.getElementById('msg');
  el.className = 'msg ' + (res.ok ? 'ok' : 'err');
  el.textContent = res.ok ? (data.message||data.apiKey||'{{.MsgDone}}') : (data.error||'{{.MsgFailed}}');
  if (res.ok) setTimeout(()=>location.reload(), 900);
}
function addDom(){ act('domain', {email, domain: document.getElementById('dom').value, remove:false}); }
function delDom(){ act('domain', {email, domain: document.getElementById('dom').value, remove:true}); }
function copyKey(btn){
  const v = document.getElementById('apikey').value;
  const done = ()=>{ btn.textContent='{{.MsgCopied}}'; setTimeout(()=>{btn.textContent='{{T "me.key.copy"}}';},1500); };
  if (navigator.clipboard && navigator.clipboard.writeText) { navigator.clipboard.writeText(v).then(done, done); } else { done(); }
}
</script>
<div class="langrow">{{.LangSwitch}}</div>
</body></html>`

	lang := pickLang(r)
	tr := func(k string) string { return T(lang, k) }
	subLine := strings.ReplaceAll(tr("me.sub"), "{email}", "<b>"+acct.Email+"</b>")
	if acct.Provider != "" {
		subLine += " via " + acct.Provider
	}
	if acct.Name != "" {
		subLine += " &middot; " + acct.Name
	}
	maxDomainsLine := tr("me.plan.random")
	if maxDomains > 0 {
		maxDomainsLine = strings.ReplaceAll(tr("me.plan.maxdom"), "{n}", fmt.Sprintf("%d", maxDomains))
	}
	expiresLine := ""
	if lbl := expiresLabel(acct.ExpiresAt); lbl != "" {
		expiresLine = strings.ReplaceAll(tr("me.plan.expires"), "{date}", lbl)
	}
	tpl := template.Must(template.New("m").Funcs(template.FuncMap{
		"T": func(key string) string { return T(lang, key) },
	}).Parse(meHTML))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = tpl.Execute(w, map[string]any{
		"Email":          acct.Email,
		"SubLine":        template.HTML(subLine),
		"Provider":       acct.Provider,
		"Name":           acct.Name,
		"EmailJS":        template.JS(fmt.Sprintf("%q", acct.Email)),
		"Tier":           acct.Tier,
		"Key":            acct.APIKey,
		"MaxDomains":     maxDomains,
		"MaxDomainsLine": template.HTML(maxDomainsLine),
		"ExpiresLine":    template.HTML(expiresLine),
		"DomainList":     acct.Domains,
		"Expires":        expiresLabel(acct.ExpiresAt),
		"ZoneConfirm":    tr("me.zone.confirm"),
		"MsgDone":        tr("me.msg.done"),
		"MsgFailed":      tr("me.msg.failed"),
		"MsgCopied":      tr("btn.copied"),
		"Lang":           lang, "Dir": langDir(lang),
		"LangSwitch": template.HTML(langSwitchHTML(lang)),
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
