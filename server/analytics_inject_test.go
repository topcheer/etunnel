package server

// Analytics injection guard: the official landing page must carry the
// analytics.ggcode.dev snippet exactly once; tenant-facing error pages
// (badGateway/notFoundHTML render on visitor-controlled *.domain hosts)
// must NOT carry it.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnalyticsSnippetOnOfficialPagesOnly(t *testing.T) {
	cfg := Config{Domain: "t.test", WSPath: "/tunnel"}
	const marker = "analytics.ggcode.dev/script.js"
	const websiteID = `data-website-id="34638151-6e32-421d-bfe0-a364824539b9"`

	landing := landingHTML(cfg, "en")
	if n := strings.Count(landing, marker); n != 1 || !strings.Contains(landing, websiteID) {
		t.Fatalf("landing analytics marker count=%d id=%v", n, strings.Contains(landing, websiteID))
	}

	nf := notFoundHTML(cfg, httptest.NewRequest(http.MethodGet, "https://zz.t.test/x", nil))
	if strings.Contains(nf, "analytics.ggcode.dev") {
		t.Fatal("404 page must not embed analytics (tenant domain)")
	}

	s := &Server{}
	rec := httptest.NewRecorder()
	s.badGateway(rec, "zz.t.test:443", "upstream down")
	if strings.Contains(rec.Body.String(), "analytics.ggcode.dev") {
		t.Fatal("502 page must not embed analytics (tenant domain)")
	}
}
