package server

// Legal pages: /terms and /privacy. Plain prose on the shared portalCSS so they
// look like the rest of the site. Content is written to cover the baseline
// obligations of GDPR (lawful basis, data subject rights, retention,
// processors) and CCPA/CPRA (categories, no sale, opt-out contact) for a
// service that stores an email, an API key and payment tier.

import (
	"fmt"
	"net/http"
	"time"
)

// legalHTML renders one legal document with the shared chrome. The document
// body is English on purpose (legal precision; the English version governs),
// but the page chrome - title, nav, footer, language switcher, lang/dir - is
// localized like the rest of the site, with a note pointing at the governing
// English text.
func (s *Server) legalHTML(w http.ResponseWriter, lang, title, updated, body string) {
	tr := func(key string) string { return T(lang, key) }
	tpl := `<!doctype html><html lang="` + lang + `" dir="` + langDir(lang) + `"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="icon" href="` + portalFavicon + `">
<title>` + title + ` - etunnel</title>
<style>` + portalCSS + `
.legal{max-width:760px;margin:0 auto;padding:2.5rem 1.5rem}
.legal h1{font-size:1.5rem;margin:0 0 .3rem}
.legal .updated{color:var(--dim);font-size:.8rem;margin-bottom:2rem}
.legal h2{font-size:1rem;margin:1.8rem 0 .5rem}
.legal p,.legal li{color:var(--dim);font-size:.88rem;line-height:1.7}
.legal ul{padding-left:1.2rem;margin:.4rem 0}
.legal strong{color:var(--fg)}
.legal a{color:#58a6ff}
.legal .ennote{border:1px solid var(--line);border-radius:8px;padding:.6rem .9rem;margin-bottom:1.6rem;font-size:.76rem}
</style></head><body>
<nav>
  <a class="logo" href="/" style="text-decoration:none">` + logoMark + ` <span>etunnel</span></a>
  <div class="links">
    <a href="/#quickstart">` + tr("nav.quickstart") + `</a>
    <a href="/pricing">` + tr("nav.pricing") + `</a>
    <a href="/terms">` + tr("foot.terms") + `</a>
    <a href="/privacy">` + tr("foot.privacy") + `</a>
  </div>
</nav>
<main class="legal">
<h1>` + title + `</h1>
<p class="updated">` + tr("legal.updated") + `: ` + updated + `</p>
<p class="ennote">` + tr("legal.en.note") + `</p>
` + body + `
</main>
<footer class="foot">` + s.cfg.Domain + ` &middot; <a href="/">` + tr("foot.home") + `</a> &middot; <a href="/terms">` + tr("foot.terms") + `</a> &middot; <a href="/privacy">` + tr("foot.privacy") + `</a>
<div class="langrow">` + langSwitchHTML(lang) + `</div></footer>
</body></html>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprint(w, tpl)
}

// ownerName is the operator identity used in both documents. Etunnel is a
// self-hosted product, so the operator is whoever runs this instance - the
// domain is the only stable identifier we can print.
func (s *Server) ownerName() string { return s.cfg.Domain }

func (s *Server) handleTerms(w http.ResponseWriter, r *http.Request) {
	d := s.cfg.Domain
	body := `
<p>These terms govern your use of the etunnel tunnel service at <strong>` + d + `</strong>
("the Service"), operated by the maintainers of <strong>` + s.ownerName() + `</strong>
("we", "us"). By creating a tunnel, an account, or a paid subscription you agree
to them. If you do not agree, do not use the Service.</p>

<h2>1. What the Service does</h2>
<p>The Service exposes a service running on your own machine to the public
internet over a temporary HTTPS hostname. We do not host, review or control the
content you serve, and we do not provide your application: you are the operator
of what the tunnel points at.</p>

<h2>2. Accounts and API keys</h2>
<ul>
<li>Anonymous tunnels need no account. They are limited by lifetime, bandwidth
and a randomly assigned hostname, and may be closed or rate-limited at any time
to protect the Service.</li>
<li>Paid plans are personal to the account email. Your API key authenticates
your client; treat it as a password. Anything done with your key is your
responsibility.</li>
<li>You must be old enough to form a binding contract in your jurisdiction
(at least 16 in the EU/UK, 13 in the US, or the age of majority where higher).</li>
</ul>

<h2>3. Acceptable use</h2>
<p><strong>You must use the Service lawfully.</strong> Using etunnel to carry
out, facilitate, or promote any activity that is illegal - under the law
applicable to you, to the operator, or to your visitors - is strictly
prohibited. Violations are grounds for immediate suspension or termination
without refund, and we will cooperate with competent authorities as required
by law.</p>
<p>You may not use the Service to:</p>
<ul>
<li>commit or facilitate criminal acts, including fraud, identity theft,
trafficking (weapons, drugs, persons), illegal gambling, or circumvention of
sanctions and export controls;</li>
<li>trade in regulated goods or services without the licences the applicable
law requires;</li>
<li>serve content that is illegal where you or your visitors are located,
including malware, phishing, child sexual abuse material, or content that
infringes others' rights;</li>
<li>attack, scan, or evade the security of third parties, or relay abusive
traffic through the tunnel;</li>
<li>circumvent the plan limits, resell raw tunnel capacity, or run denial-of-
service or bulk-credential-stuffing traffic;</li>
<li>impersonate another service in a way that would deceive its users
(phishing with a look-alike hostname).</li>
</ul>
<p>We may suspend or terminate any tunnel or account that violates these rules,
and will cooperate with lawful requests from competent authorities.</p>

<h2>4. Plans, payment, renewal and refunds</h2>
<ul>
<li>Paid plans are sold per month and activate the moment payment clears. Your
plan is active until the expiry shown in your account, after which it lapses to
the free tier automatically.</li>
<li>Payments are processed by third parties (PayPal, NOWPayments, or an
equivalent provider enabled by the operator). We never see or store your card
number or wallet keys; the processor's own terms apply to the payment.</li>
<li>Subscriptions do not auto-renew: you pay again when you want another period.
Cancelling stops any future charge and keeps the plan until it expires.</li>
<li>Because a paid period is consumed by using the Service, fees are
non-refundable except where the law requires it (for example the EU 14-day
withdrawal right does not apply once the digital service has started with your
consent, which you give by activating the plan).</li>
</ul>

<h2>5. Availability, changes, termination</h2>
<p>The Service is provided on a best-effort basis. We aim to keep it running but
do not guarantee uptime, and we may change or discontinue features, limits or
the whole Service with reasonable notice. You may stop using it at any time,
and you can close your account from your account page.</p>

<h2>6. Your content and our liability</h2>
<p>You keep all rights to what you serve. To the maximum extent permitted by
law, the Service is provided "as is" without warranties, and our aggregate
liability for any claim relating to the Service is limited to the amount you
paid us in the twelve months before the claim.</p>

<h2>7. Governing law</h2>
<p>These terms are governed by the laws of the operator's place of
establishment, without affecting consumer rights you have where you live.</p>

<h2>8. Contact</h2>
<p>Questions about these terms: <a href="mailto:info@` + d + `">info@` + d + `</a>.</p>
`
	lang := pickLang(r)
	s.legalHTML(w, lang, T(lang, "terms.title"), time.Now().Format("2006-01-02"), body)
}

func (s *Server) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	d := s.cfg.Domain
	body := `
<p>This notice explains what personal data <strong>` + d + `</strong> processes,
why, and the rights you have. It covers the tunnel service, the account portal
and the payment flow.</p>

<h2>1. What we collect, and why</h2>
<ul>
<li><strong>Account email</strong> - from your sign-in provider (Google, GitLab,
GitHub, Apple) or from the address you enter at checkout. Used to identify your
account, deliver the API key, and contact you about the Service. Lawful basis:
performance of a contract.</li>
<li><strong>Sign-in identifier</strong> - the provider's stable user id plus your
display name, so a later sign-in resolves to the same account.</li>
<li><strong>API key</strong> - a random token that authenticates your client. It
is the credential for your tunnels.</li>
<li><strong>Plan and subscription state</strong> - which tier is active and when
it expires.</li>
<li><strong>Custom subdomains</strong> - the names you register on your account.</li>
<li><strong>Technical data</strong> - the IP address and User-Agent of connecting
clients, request metadata for tunnels, and aggregate byte counts, kept to
operate the service, rate-limit abuse and diagnose faults. Lawful basis:
legitimate interest in running a secure service.</li>
<li><strong>Session cookie</strong> - one HttpOnly cookie (<code>et_session</code>)
holding a signed value of your email, so you stay logged in. It is strictly
necessary; we use no analytics, advertising or third-party tracking cookies.</li>
</ul>
<p>We do not ask for and do not store card numbers, bank details or wallet keys.
Payment credentials are entered on the payment provider's own pages.</p>

<h2>2. Who else processes it</h2>
<ul>
<li><strong>Your sign-in provider</strong> (Google, GitLab, GitHub or Apple)
authenticates you and shares your email and display name with us.</li>
<li><strong>Payment providers</strong> (PayPal, NOWPayments) process the payment
and tell us that it succeeded, together with the email and plan that were
attached to the order.</li>
<li><strong>DNS and CDN providers</strong> (for example Cloudflare) resolve the
domain and may see visitor IP addresses as any authoritative DNS or edge
provider does.</li>
<li>We do not sell personal data, and we do not share it with advertisers or
data brokers.</li>
</ul>

<h2>3. How long we keep it</h2>
<ul>
<li>Account and plan data: while the account exists, and afterwards only as long
as needed for accounting and legal obligations. An account that has never paid
and has been inactive may be removed.</li>
<li>An account that has ever paid is retained after it lapses so that support,
refund requests and re-subscription remain possible.</li>
<li>Session cookies expire after 30 days or when you sign out.</li>
<li>Request logs are short-lived operational data and are rotated.</li>
</ul>

<h2>4. Your rights</h2>
<p>Depending on where you live you have some or all of the following rights:
access, correction, deletion, restriction, portability, objection, and the right
to lodge a complaint with your supervisory authority (GDPR); the right to know,
delete, correct and to opt out of "sharing" or "selling" (CCPA/CPRA - we do
neither), and not to be discriminated against for exercising them.</p>
<ul>
<li><strong>Access and correction</strong>: your account page shows the email,
plan, expiry and custom subdomains we hold.</li>
<li><strong>Deletion</strong>: "Close account" on the account page deletes the
account, its API key and its subdomains immediately.</li>
<li><strong>Anything else</strong>: write to
<a href="mailto:info@` + d + `">info@` + d + `</a> and we will respond within
30 days.</li>
</ul>

<h2>5. International transfers and security</h2>
<p>The service runs on servers in the region chosen by its operator; your data
may therefore be processed outside your country. Transfers rely on the
safeguards offered by our processors (standard contractual clauses where
applicable). We use TLS for all traffic, hash-free random API keys (never
derived from your email) and restrict access to the server to its operator.</p>

<h2>6. Children</h2>
<p>The Service is not directed at children and we do not knowingly collect data
from anyone below the age of digital consent in their country.</p>

<h2>7. Changes</h2>
<p>If this notice changes materially we will note the new date at the top and,
for significant changes, mention it in the account page.</p>

<h2>8. Contact</h2>
<p>Operator of this instance: <strong>` + s.ownerName() + `</strong>.
Privacy questions and requests: <a href="mailto:info@` + d + `">info@` + d + `</a>.</p>
`
	lang := pickLang(r)
	s.legalHTML(w, lang, T(lang, "privacy.title"), time.Now().Format("2006-01-02"), body)
}
