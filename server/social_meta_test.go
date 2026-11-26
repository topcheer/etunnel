package server

import (
	"strings"
	"testing"
)

func TestLandingHasSocialMeta(t *testing.T) {
	cfg := Config{Domain: "t.example.com", WSPath: "/tunnel"}
	html := landingHTML(cfg, "en")
	for _, want := range []string{
		`<meta property="og:image" content="https://t.example.com/assets/og-image.png">`,
		`<meta property="og:title" content="etunnel - your localhost, on the internet">`,
		`<meta property="og:type" content="website">`,
		`<meta property="og:image:width" content="1200">`,
		`<meta name="twitter:card" content="summary_large_image">`,
		`<link rel="canonical" href="https://t.example.com/">`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("landing missing %s", want)
		}
	}
	// The SDK count in the meta description must track the real number
	// (22 SDKs incl. Android + HarmonyOS), not the pre-1.7 "20".
	if !strings.Contains(html, "22 language SDKs") || strings.Contains(html, "20 language SDKs") {
		t.Error("landing meta description advertises the wrong SDK count")
	}
}

func TestSocialMetaEscapesAttributesAndPinsBaseDomain(t *testing.T) {
	cfg := Config{Domain: "t.example.com"}
	out := socialMetaHTML(cfg, `a "quoted" <title>`, "desc & more")
	if !strings.Contains(out, `og:title" content="a &#34;quoted&#34; &lt;title&gt;"`) {
		t.Errorf("og:title not attribute-escaped: %s", out)
	}
	if !strings.Contains(out, `og:image" content="https://t.example.com/assets/og-image.png"`) ||
		!strings.Contains(out, `og:url" content="https://t.example.com/"`) {
		t.Errorf("og tags must be absolute URLs on the base domain: %s", out)
	}
}

func TestSocialMetaEveryLanguageAdvertises22SDKs(t *testing.T) {
	for _, lang := range uiLangs {
		desc := T(lang, "land.meta.desc")
		if strings.Contains(desc, " 20 ") || strings.Contains(desc, "20言") ||
			strings.Contains(desc, "20개") || strings.Contains(desc, "لعشرين لغة") {
			t.Errorf("%s: land.meta.desc still says 20 SDKs: %s", lang, desc)
		}
	}
}
