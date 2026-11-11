package server

// i18n acceptance: negotiation precedence, dictionary completeness (every
// language covers every English key), RTL for Arabic, and page rendering in
// a negotiated language.

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func reqWith(header, val string, cookies ...*http.Cookie) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if header != "" {
		r.Header.Set(header, val)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	return r
}

func TestPickLangNegotiation(t *testing.T) {
	cases := []struct {
		name   string
		cookie string
		header string
		want   string
	}{
		{"default en", "", "", "en"},
		{"simple ja", "", "ja", "ja"},
		{"zh-cn variant", "", "zh-CN,zh;q=0.9", "zh-CN"},
		{"zh-hans maps", "", "zh-Hans", "zh-CN"},
		{"q ordering", "", "fr;q=0.3,de;q=0.9", "de"},
		{"q=0 drops", "", "de;q=0,fr", "fr"},
		{"unknown falls through", "", "xx-YY,es", "es"},
		{"region direct", "", "pt-BR", "pt-BR"},
		{"bare base to primary", "", "pt", "pt-BR"},
		{"us english", "", "en-US,en;q=0.9", "en"},
		{"cookie wins over header", "ja", "de", "ja"},
		{"bad cookie falls to header", "xx", "ko", "ko"},
		{"arabic", "", "ar-EG,ar;q=0.9", "ar"},
	}
	for _, c := range cases {
		var cookies []*http.Cookie
		if c.cookie != "" {
			cookies = append(cookies, &http.Cookie{Name: "et_lang", Value: c.cookie})
		}
		if got := pickLang(reqWith("Accept-Language", c.header, cookies...)); got != c.want {
			t.Errorf("%s: pickLang = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestI18nCompleteness: every registered language must cover exactly the
// English key set - a missing key means an English leak into a translated
// page; an extra key is dead weight.
func TestI18nCompleteness(t *testing.T) {
	en := dicts["en"]
	if len(en) == 0 {
		t.Fatal("empty English reference dictionary")
	}
	for _, l := range uiLangs {
		d, ok := dicts[l]
		if !ok {
			t.Errorf("language %q registered in uiLangs but has no dictionary", l)
			continue
		}
		for k := range en {
			if v, ok := d[k]; !ok || strings.TrimSpace(v) == "" {
				t.Errorf("%s: missing/empty key %q", l, k)
			}
		}
		for k := range d {
			if _, ok := en[k]; !ok {
				t.Errorf("%s: extra key %q not in English reference", l, k)
			}
		}
	}
}

func TestLangDir(t *testing.T) {
	if langDir("ar") != "rtl" || langDir("en") != "ltr" || langDir("zh-CN") != "ltr" {
		t.Fatal("langDir broken")
	}
}

func TestLangSwitchHTML(t *testing.T) {
	h := langSwitchHTML("en")
	if !strings.Contains(h, `<b>English</b>`) || !strings.Contains(h, `/setlang?l=ja`) || !strings.Contains(h, "日本語") {
		t.Fatalf("switcher malformed: %s", h)
	}
	// current language must not be a link (no self-link)
	if strings.Count(langSwitchHTML("ja"), `/setlang?l=ja"`) != 0 {
		t.Fatal("current language must be bold text, not a link")
	}
}

// TestLandingRenderedInLanguage: the landing page honors the negotiated
// language end to end - html lang/dir attributes and translated hero copy.
func TestLandingRenderedInLanguage(t *testing.T) {
	s, err := New(Config{
		Domain: "t.test", WSPath: "/tunnel",
		TLS:          TLSConfig{Mode: TLSModeHTTP},
		AccountsFile: filepath.Join(t.TempDir(), "a.json"),
		Tiers:        DefaultTiers(),
	})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct{ name, cookie, wantSub, dir string }{
		{"english default", "", "your localhost", "ltr"},
		{"chinese via cookie", "zh-CN", "本地", "ltr"},
	}
	for _, c := range cases {
		r := reqWith("", "", &http.Cookie{Name: "et_lang", Value: c.cookie})
		if c.cookie == "" {
			r.Header.Set("Accept-Language", "en")
		}
		html := landingHTML(s.cfg, pickLang(r))
		if !strings.Contains(html, `dir="`+c.dir+`"`) {
			t.Errorf("%s: html dir attribute missing/wrong", c.name)
		}
		if !strings.Contains(html, c.wantSub) {
			t.Errorf("%s: hero copy not translated (missing %q)", c.name, c.wantSub)
		}
		if !strings.Contains(html, "langsw") {
			t.Errorf("%s: language switcher missing", c.name)
		}
	}
}

// TestSetLangCookieAndRedirect: the switcher endpoint sets the cookie and
// bounces back; a foreign referer is refused (no open redirect).
func TestSetLangCookieAndRedirect(t *testing.T) {
	s, err := New(Config{
		Domain: "t.test", WSPath: "/tunnel",
		TLS:          TLSConfig{Mode: TLSModeHTTP},
		AccountsFile: filepath.Join(t.TempDir(), "a.json"),
		Tiers:        DefaultTiers(),
	})
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "/setlang?l=ja", nil)
	r.Header.Set("Referer", "http://t.test/pricing")
	rec := httptest.NewRecorder()
	s.handleSetLang(rec, r)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("redirect: %d", rec.Code)
	}
	var got string
	for _, c := range rec.Result().Cookies() {
		if c.Name == "et_lang" {
			got = c.Value
		}
	}
	if got != "ja" {
		t.Fatalf("cookie = %q, want ja", got)
	}

	r2 := httptest.NewRequest(http.MethodGet, "/setlang?l=ja", nil)
	r2.Header.Set("Referer", "https://evil.example/phish")
	rec2 := httptest.NewRecorder()
	s.handleSetLang(rec2, r2)
	if loc := rec2.Header().Get("Location"); loc != "/" {
		t.Fatalf("foreign referer must redirect home, got %q", loc)
	}
}

// TestPricingAndBillingRenderedInLanguage: the payments-facing pages
// (pricing table + billing success) honor the negotiated language and do
// not leak the English reference copy for the same keys.
func TestPricingAndBillingRenderedInLanguage(t *testing.T) {
	s, err := New(Config{
		Domain: "t.test", WSPath: "/tunnel",
		TLS:          TLSConfig{Mode: TLSModeHTTP},
		AccountsFile: filepath.Join(t.TempDir(), "a.json"),
		Tiers:        DefaultTiers(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Pricing page: German via cookie.
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/pricing", nil)
	r.AddCookie(&http.Cookie{Name: "et_lang", Value: "de"})
	s.handlePricing(rec, r)
	body := rec.Body.String()
	if !strings.Contains(body, "Preise") {
		t.Errorf("pricing page not translated (missing German %q)", "Preise")
	}
	if strings.Contains(body, "<h1>Pricing</h1>") {
		t.Error("pricing page leaked the English h1 alongside the German one")
	}
	if !strings.Contains(body, "langsw") {
		t.Error("language switcher missing on pricing page")
	}

	// Billing success page: German via cookie.
	rec = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/billing/success", nil)
	r.AddCookie(&http.Cookie{Name: "et_lang", Value: "de"})
	s.writeSuccessKeyPage(rec, r, "starter", "u@example.com", "et_test_key")
	body = rec.Body.String()
	if !strings.Contains(body, "Zahlung eingegangen") {
		t.Error("billing success page not translated (missing German bill.h1)")
	}
	if strings.Contains(body, "Payment received") {
		t.Error("billing success page leaked the English bill.h1")
	}
}
