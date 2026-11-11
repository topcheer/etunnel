package server

// Custom-subdomain input handling: users paste full hostnames
// ("my-sub.t.etun.uk") or type short labels into the "my-sub" field. The
// endpoint normalizes the hostname case away and, when it still cannot be a
// subdomain, explains why instead of an opaque 400.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeSubdomainInput(t *testing.T) {
	cases := map[string]string{
		"my-sub":                     "my-sub",
		"  My-Sub  ":                 "my-sub",
		"my-sub.t.etun.uk":           "my-sub", // service suffix stripped
		"https://my-sub.t.etun.uk":   "my-sub", // scheme + suffix
		"https://my-sub.t.etun.uk/x": "my-sub", // + path
		"my-sub.com":                 "my-sub", // foreign hostname: first label
		"my-sub.example.dev":         "my-sub",
		"-my-sub-":                   "my-sub",
		"ab":                         "ab",
	}
	for in, want := range cases {
		if got := normalizeSubdomainInput(in, "t.etun.uk"); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDomainAddAcceptsShortAndExplainsRejects(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	if _, err := s.activate("u@x.com", "pro"); err != nil { // pro: 5 domains headroom
		t.Fatal(err)
	}
	post := func(dom string) *httptest.ResponseRecorder {
		body := `{"email":"u@x.com","domain":"` + dom + `","remove":false}`
		r := httptest.NewRequest(http.MethodPost, "/me/domain", bytes.NewBufferString(body))
		r.AddCookie(s.sessionCookie("u@x.com"))
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.handleMeAction(rec, r, "domain")
		return rec
	}

	// two characters now accepted (was >= 3; users typed "ab" and hit 400)
	if rec := post("ab"); rec.Code != http.StatusOK {
		t.Fatalf("2-char subdomain: got %d %s", rec.Code, rec.Body.String())
	}
	// pasted full hostname resolves to the label and is added
	if rec := post("my-sub.t.etun.uk"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "my-sub") {
		t.Fatalf("full hostname: got %d %s", rec.Code, rec.Body.String())
	}
	// single characters are valid since the 1-63 relaxation
	if rec := post("a"); rec.Code != http.StatusOK {
		t.Fatalf("1-char: got %d %s", rec.Code, rec.Body.String())
	}
	// illegal characters get the no-dots explanation, echoed via writeJSON
	rec := post("my sub")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "a-z, 0-9") {
		t.Fatalf("space: got %d %s", rec.Code, rec.Body.String())
	}
}
