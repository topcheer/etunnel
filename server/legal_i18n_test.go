package server

// The legal pages localize their chrome (title, updated label, nav, footer,
// language switcher) like every other user-facing page, and render lang/dir
// from pickLang. The document bodies stay English by design.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegalChromeLocalized(t *testing.T) {
	s := newBillingTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/terms", nil)
	r.Header.Set("Accept-Language", "zh-CN")
	rec := httptest.NewRecorder()
	s.handleTerms(rec, r)
	body := rec.Body.String()
	if !strings.Contains(body, `<html lang="zh-CN" dir="ltr">`) {
		t.Fatalf("terms missing zh-CN lang attr: %.200s", body)
	}
	if !strings.Contains(body, "服务条款") || !strings.Contains(body, "最后更新") {
		t.Fatal("terms chrome not localized for zh-CN")
	}
	if !strings.Contains(body, "以英文版本为准") {
		t.Fatal("terms missing governing-language note for zh-CN")
	}
	if !strings.Contains(body, "langsw") {
		t.Fatal("terms missing language switcher")
	}
	if !strings.Contains(body, "You must use the Service lawfully") {
		t.Fatal("terms body must stay English (governing version)")
	}

	r = httptest.NewRequest(http.MethodGet, "/privacy", nil)
	r.Header.Set("Accept-Language", "ar")
	rec = httptest.NewRecorder()
	s.handlePrivacy(rec, r)
	body = rec.Body.String()
	if !strings.Contains(body, `<html lang="ar" dir="rtl">`) {
		t.Fatalf("privacy missing ar rtl attr: %.200s", body)
	}
	if !strings.Contains(body, "سياسة الخصوصية") {
		t.Fatal("privacy chrome not localized for ar")
	}
}
