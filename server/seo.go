package server

import (
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// Search-engine and AI-crawler surface of the base site (SEO/GEO):
//
//   - robots.txt / sitemap.xml / llms.txt / favicon.ico as real files with
//     the right content types (previously every unknown base path answered
//     200 with the landing page - a soft-404 that drowned the domain in
//     duplicate HTML and handed crawlers an HTML pseudo-robots.txt).
//   - Per-language landing URLs (/zh/, /ja/, ...) so translated copy is
//     indexable, with hreflang alternates; the cookie switcher stays for
//     preference memory only.
//   - JSON-LD (SoftwareApplication + Organization + WebSite + FAQPage) and a
//     server-rendered FAQ section, which is what rich results and generative
//     engines read for "what is it / how much / how do I use it".
//
//go:embed llms.txt
var llmsTxt string

// langPath is the public URL prefix of a UI language ("" for the default,
// language-negotiated "/"). Crawlable, lowercase; pt-BR is served as /pt-br/.
func langPath(lang string) string {
	switch lang {
	case "en":
		return ""
	case "zh-CN":
		return "/zh/" // the short, conventional path; hreflang keeps zh-CN
	}
	return "/" + strings.ToLower(lang) + "/"
}

// langFromPath maps a request path onto a UI language ("" when the path is
// not a language landing). Only the exact landing paths match; "/zh/anything"
// is a 404 like every other unknown base path.
func langFromPath(p string) string {
	if p == "/" {
		return "en" // negotiated by cookie / Accept-Language in the caller
	}
	for _, l := range uiLangs {
		if l != "en" && p == langPath(l) {
			return l
		}
	}
	return ""
}

// landingPath is the canonical URL of a language landing.
func landingPath(lang string) string {
	if lang == "en" {
		return "/"
	}
	return langPath(lang)
}

func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString("User-agent: *\nAllow: /\n")
	for _, p := range []string{"/me", "/login", "/logout", "/setlang", "/billing", "/auth/", "/api/", "/webhooks/", "/admin"} {
		b.WriteString("Disallow: " + p + "\n")
	}
	b.WriteString("\nSitemap: https://" + s.cfg.Domain + "/sitemap.xml\n")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(b.String()))
}

func (s *Server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	base := "https://" + s.cfg.Domain
	var b strings.Builder
	b.WriteString(xmlHeader)
	for _, l := range uiLangs {
		b.WriteString("<url><loc>" + xmlEscape(base+landingPath(l)) + "</loc></url>\n")
	}
	for _, p := range []string{"/pricing"} {
		b.WriteString("<url><loc>" + base + p + "</loc></url>\n")
	}
	b.WriteString("</urlset>\n")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(b.String()))
}

func (s *Server) handleLLMs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(llmsTxt))
}

// handleFavicon serves the brand SVG under the conventional /favicon.ico
// path with an SVG content type (modern browsers and crawlers accept it; a
// 404 here is a noisy error in every access log).
// baseNotFound is the 404 for the base domain: no index, no auto-redirect
// (the tenant "tunnel not found" page bounces to GitHub, which is wrong for a
// mistyped site URL), link back home.
func (s *Server) baseNotFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>404 - page not found</title></head><body style="font-family:system-ui,sans-serif;background:#f8f3e7;color:#242b21;display:flex;min-height:100vh;align-items:center;justify-content:center;margin:0">
<main style="max-width:520px;padding:2rem;text-align:center"><h1 style="font-size:1.6rem">404</h1>
<p style="color:#7a7f6e">This page does not exist on <code>`+htmlEscapeText(s.cfg.Domain)+`</code>.</p>
<p><a style="color:#2c6e49" href="/">Back to the homepage</a> &middot; <a style="color:#2c6e49" href="https://github.com/topcheer/etunnel">GitHub</a></p></main></body></html>`)
}

func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.Redirect(w, r, "/assets/icon.svg", http.StatusMovedPermanently)
}

// xmlHeader carries the sitemap schema namespace (an identifier, never fetched).
const xmlHeader = `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
`

// hreflangHTML lists every language landing plus x-default for the
// language-negotiated root.
func hreflangHTML(cfg Config, self string) string {
	base := "https://" + cfg.Domain
	var b strings.Builder
	b.WriteString(`<link rel="alternate" hreflang="x-default" href="` + base + `/">` + "\n")
	for _, l := range uiLangs {
		href := base + landingPath(l)
		if l == "en" {
			// en is the x-default target; its canonical landing is "/" too.
			b.WriteString(`<link rel="alternate" hreflang="en" href="` + href + `">` + "\n")
			continue
		}
		b.WriteString(`<link rel="alternate" hreflang="` + htmlEscapeText(l) + `" href="` + href + `">` + "\n")
	}
	_ = self
	return b.String()
}

// faqHTML renders the FAQ as plain server-side HTML (visible without JS).
func faqHTML(lang string) string {
	var b strings.Builder
	b.WriteString(`<section id="faq"><h2>` + T(lang, "land.faq.h") + `</h2>`)
	for i := 1; i <= 5; i++ {
		b.WriteString(`<div class="card faq"><h3>` + T(lang, "land.faq.q"+itoa(i)) + `</h3><p>` + T(lang, "land.faq.a"+itoa(i)) + `</p></div>`)
	}
	b.WriteString(`</section>`)
	return b.String()
}

func itoa(i int) string { return string(rune('0' + i)) }

// jsonLDFaq builds the FAQPage graph from the same strings as the HTML.
func jsonLDFaq(lang string) string {
	var b strings.Builder
	b.WriteString(`{"@context":"https://schema.org","@type":"FAQPage","mainEntity":[`)
	for i := 1; i <= 5; i++ {
		if i > 1 {
			b.WriteString(",")
		}
		b.WriteString(`{"@type":"Question","name":` + jsonQuote(stripTags(T(lang, "land.faq.q"+itoa(i)))) +
			`,"acceptedAnswer":{"@type":"Answer","text":` + jsonQuote(stripTags(T(lang, "land.faq.a"+itoa(i)))) + `}}`)
	}
	b.WriteString(`]}`)
	return b.String()
}

// jsonLDSoftwareApplication describes the product for rich results and
// generative engines: free anonymous tier plus paid plans.
func jsonLDSoftwareApplication(cfg Config, lang string) string {
	return `[{"@context":"https://schema.org","@type":"SoftwareApplication","name":"etunnel","applicationCategory":"DeveloperApplication",` +
		`"operatingSystem":"Linux, macOS, Windows, Android, HarmonyOS, iOS",` +
		`"description":` + jsonQuote(stripTags(T(lang, "land.sub"))) + `,` +
		`"url":"https://` + cfg.Domain + `/","image":"https://` + cfg.Domain + `/assets/` + ogImageFile + `",` +
		`"offers":{"@type":"AggregateOffer","priceCurrency":"USD","lowPrice":"0","highPrice":"9","offerCount":"3"},` +
		`"featureList":"Instant public HTTPS URL, automatic TLS, 22 SDKs, reconnect keeps your URL, WebSocket pass-through, self-hostable server"}` +
		`,{"@context":"https://schema.org","@type":"Organization","name":"etunnel","url":"https://` + cfg.Domain + `/","sameAs":["https://github.com/topcheer/etunnel"]}` +
		`,{"@context":"https://schema.org","@type":"WebSite","name":"etunnel","url":"https://` + cfg.Domain + `/","inLanguage":"` + htmlEscapeText(lang) + `"}]`
}

func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}
