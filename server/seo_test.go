package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// seoTestServer builds a server whose base domain is t.test.
func seoTestServer(t *testing.T) *Server {
	t.Helper()
	return obsTestServer(t, func(c *Config) { c.Domain = "t.test" })
}

func getBody(t *testing.T, s *Server, path string, hdr map[string]string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://t.test"+path, nil)
	req.Host = "t.test"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec, rec.Body.String()
}

func TestCrawlFilesAreRealFiles(t *testing.T) {
	s := seoTestServer(t)
	rec, body := getBody(t, s, "/robots.txt", nil)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("robots.txt: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, want := range []string{"User-agent: *", "Allow: /", "Disallow: /me", "Sitemap: https://t.test/sitemap.xml"} {
		if !strings.Contains(body, want) {
			t.Errorf("robots.txt missing %q", want)
		}
	}
	if strings.Contains(strings.ToLower(body), "<html") {
		t.Error("robots.txt must not be HTML")
	}

	rec, body = getBody(t, s, "/sitemap.xml", nil)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/xml") {
		t.Fatalf("sitemap.xml: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, want := range []string{"<loc>https://t.test/</loc>", "<loc>https://t.test/zh/</loc>", "<loc>https://t.test/ja/</loc>", "<loc>https://t.test/pt-br/</loc>", "<loc>https://t.test/pricing</loc>"} {
		if !strings.Contains(body, want) {
			t.Errorf("sitemap missing %s", want)
		}
	}

	rec, body = getBody(t, s, "/llms.txt", nil)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") || !strings.HasPrefix(body, "# etunnel") {
		t.Fatalf("llms.txt: %d %s %q", rec.Code, rec.Header().Get("Content-Type"), body[:min(20, len(body))])
	}

	rec, _ = getBody(t, s, "/favicon.ico", nil)
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/assets/icon.svg" {
		t.Fatalf("favicon.ico: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestLLMsTxtInSyncWithRepoRoot(t *testing.T) {
	root, err := os.ReadFile("../llms.txt")
	if err != nil {
		t.Skip("repo root llms.txt not reachable from this test run")
	}
	if string(root) != llmsTxt {
		t.Error("server/llms.txt (embedded, served at /llms.txt) differs from the repo-root llms.txt - keep both identical")
	}
}

// TestBaseDomainUnknownPathIs404 is the soft-404 regression: every unknown
// base-domain path used to answer 200 with the landing page.
func TestBaseDomainUnknownPathIs404(t *testing.T) {
	s := seoTestServer(t)
	for _, p := range []string{"/nope", "/admin", "/zh/anything", "/pricing/extra", "/robots.txt/x"} {
		rec, body := getBody(t, s, p, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d", p, rec.Code)
		}
		if !strings.Contains(body, `name="robots" content="noindex"`) {
			t.Errorf("%s: 404 page must be noindex", p)
		}
	}
	rec, _ := getBody(t, s, "/", nil)
	if rec.Code != 200 {
		t.Fatalf("/ must stay 200, got %d", rec.Code)
	}
	// Reserved endpoints keep working next to the catch-all.
	rec, _ = getBody(t, s, "/pricing", nil)
	if rec.Code != 200 {
		t.Fatalf("/pricing: %d", rec.Code)
	}
}

func TestLanguageLandingsAreCrawlable(t *testing.T) {
	s := seoTestServer(t)
	for lang, want := range map[string]string{"zh-CN": "/zh/", "ja": "/ja/", "de": "/de/", "ar": "/ar/", "pt-BR": "/pt-br/"} {
		// A foreign cookie must not override the explicit language URL.
		rec, body := getBody(t, s, want, map[string]string{"Cookie": "et_lang=en"})
		if rec.Code != 200 {
			t.Fatalf("%s: %d", want, rec.Code)
		}
		if !strings.Contains(body, `<html lang="`+lang+`"`) {
			t.Errorf("%s: rendered language is not %s", want, lang)
		}
		if !strings.Contains(body, `<link rel="canonical" href="https://t.test`+want+`">`) {
			t.Errorf("%s: canonical must be the language URL", want)
		}
		if !strings.Contains(body, `property="og:url" content="https://t.test`+want+`"`) {
			t.Errorf("%s: og:url must be the language URL", want)
		}
		if !strings.Contains(body, `property="og:title" content="`+T(lang, "land.title")+`"`) {
			t.Errorf("%s: og:title not translated", want)
		}
		if !strings.Contains(body, `<h2>`+T(lang, "land.faq.h")+`</h2>`) || !strings.Contains(body, T(lang, "land.faq.a1")) {
			t.Errorf("%s: FAQ section must be server-rendered in the page language", want)
		}
		for _, l := range uiLangs {
			hreflang := l
			if l == "zh-CN" {
				hreflang = "zh-CN"
			}
			if !strings.Contains(body, `<link rel="alternate" hreflang="`+hreflang+`" href="https://t.test`+landingPath(l)+`">`) {
				t.Errorf("%s: missing hreflang %s", want, l)
			}
		}
		if !strings.Contains(body, `hreflang="x-default" href="https://t.test/"`) {
			t.Errorf("%s: missing x-default", want)
		}
	}
	// Root keeps language negotiation and points its canonical at itself.
	rec, body := getBody(t, s, "/", map[string]string{"Accept-Language": "zh-CN,zh;q=0.9"})
	if rec.Code != 200 || !strings.Contains(body, `<html lang="zh-CN"`) {
		t.Fatalf("/: root must still negotiate language, got %d", rec.Code)
	}
	if !strings.Contains(body, `<link rel="canonical" href="https://t.test/">`) {
		t.Error("/: canonical must be /")
	}
}

func TestLandingCarriesValidJSONLD(t *testing.T) {
	s := seoTestServer(t)
	_, body := getBody(t, s, "/zh/", nil)
	blocks := regexp.MustCompile(`<script type="application/ld\+json">(.*?)</script>`).FindAllStringSubmatch(body, -1)
	if len(blocks) < 2 {
		t.Fatalf("want SoftwareApplication + FAQPage JSON-LD, got %d block(s)", len(blocks))
	}
	seen := map[string]bool{}
	for _, b := range blocks {
		var v any
		if err := json.Unmarshal([]byte(htmlUnescape(b[1])), &v); err != nil {
			t.Fatalf("JSON-LD is not valid JSON: %v\n%s", err, b[1][:min(120, len(b[1]))])
		}
		seen["ok"] = true
		switch g := v.(type) {
		case []any:
			for _, e := range g {
				if m, ok := e.(map[string]any); ok {
					seen[m["@type"].(string)] = true
				}
			}
		case map[string]any:
			seen[g["@type"].(string)] = true
		}
	}
	for _, want := range []string{"SoftwareApplication", "Organization", "WebSite", "FAQPage"} {
		if !seen[want] {
			t.Errorf("missing JSON-LD @type %s", want)
		}
	}
	if !strings.Contains(body, `<meta name="theme-color" content="#f8f3e7">`) {
		t.Error("missing theme-color")
	}
}

func TestLangSwitcherLinksToLanguageURLs(t *testing.T) {
	s := seoTestServer(t)
	_, body := getBody(t, s, "/", nil)
	for _, l := range uiLangs[1:] {
		if !strings.Contains(body, `<a href="`+langPath(l)+`" hreflang="`+l+`">`) {
			t.Errorf("switcher must link to %s", langPath(l))
		}
	}
	if strings.Contains(body, `/setlang?l=`) {
		t.Error("switcher should not rely on cookie links any more")
	}
}

func htmlUnescape(s string) string {
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#34;", `"`, "&#39;", "'")
	return r.Replace(s)
}
