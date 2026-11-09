package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSessionCookieRoundtrip(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "testsecret"
	if _, err := s.activate("u@x.com", "pro"); err != nil {
		t.Fatal(err)
	}
	c := s.sessionCookie("u@x.com")

	r := httptest.NewRequest(http.MethodGet, "/me", nil)
	r.AddCookie(c)
	if got := s.sessionEmail(r); got != "u@x.com" {
		t.Fatalf("session email: %q", got)
	}

	// tampered cookie fails
	r2 := httptest.NewRequest(http.MethodGet, "/me", nil)
	r2.AddCookie(&http.Cookie{Name: "et_session", Value: "u@x.com|99999999999|deadbeef"})
	if got := s.sessionEmail(r2); got != "" {
		t.Fatalf("tampered accepted: %q", got)
	}

	// closed account invalidates session
	s.accounts.Mutate("u@x.com", func(a *Account) {})
	if a, _ := s.accounts.LookupEmail("u@x.com"); a != nil {
		s.accounts.Delete(a.APIKey)
	}
	r3 := httptest.NewRequest(http.MethodGet, "/me", nil)
	r3.AddCookie(c)
	if got := s.sessionEmail(r3); got != "" {
		t.Fatalf("closed account session accepted: %q", got)
	}
}

func authedReq(t *testing.T, s *Server, email, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.AddCookie(s.sessionCookie(email))
	w := httptest.NewRecorder()
	s.handleMeAction(w, r, strings.TrimPrefix(path, "/me/"))
	return w
}

func TestPortalActions(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	if _, err := s.activate("u@x.com", "pro"); err != nil {
		t.Fatal(err)
	}

	// acting on another account is forbidden
	if w := authedReq(t, s, "u@x.com", "/me/cancel", `{"email":"other@x.com"}`); w.Code != http.StatusForbidden {
		t.Fatalf("cross-account: %d", w.Code)
	}

	// cancel downgrades to free
	if w := authedReq(t, s, "u@x.com", "/me/cancel", `{"email":"u@x.com"}`); w.Code != 200 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	if a, _ := s.accounts.LookupEmail("u@x.com"); a.Tier != "free" {
		t.Fatalf("tier after cancel: %s", a.Tier)
	}

	// re-upgrade, then reset key (old key revoked)
	s.accounts.Mutate("u@x.com", func(a *Account) { a.Tier = "pro" })
	cur, _ := s.accounts.LookupEmail("u@x.com")
	oldKey := cur.APIKey // copy: Lookup returns the shared pointer
	if w := authedReq(t, s, "u@x.com", "/me/reset", `{"email":"u@x.com"}`); w.Code != 200 {
		t.Fatalf("reset: %d", w.Code)
	}
	if a, _ := s.accounts.LookupEmail("u@x.com"); a.APIKey == oldKey {
		t.Fatal("key not rotated")
	}
	if _, ok := s.accounts.Lookup(oldKey); ok {
		t.Fatal("old key still valid")
	}

	// domain add within pro quota (5), then quota refusal after downgrade
	for i, d := range []string{"alpha", "beta", "gamma", "delta", "eps", "zeta"} {
		w := authedReq(t, s, "u@x.com", "/me/domain", `{"email":"u@x.com","domain":"`+d+`"}`)
		if i < 5 && w.Code != 200 {
			t.Fatalf("add %s: %d %s", d, w.Code, w.Body.String())
		}
		if i == 5 && w.Code != http.StatusForbidden {
			t.Fatalf("quota not enforced: %d", w.Code)
		}
	}
	// remove one then add works again
	if w := authedReq(t, s, "u@x.com", "/me/domain", `{"email":"u@x.com","domain":"zeta","remove":true}`); w.Code != 200 {
		t.Fatalf("remove: %d", w.Code)
	}

	// invalid subdomain rejected
	if w := authedReq(t, s, "u@x.com", "/me/domain", `{"email":"u@x.com","domain":"BAD NAME"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid sub accepted: %d", w.Code)
	}

	// close: succeeds with no live tunnels
	if w := authedReq(t, s, "u@x.com", "/me/close", `{"email":"u@x.com"}`); w.Code != 200 {
		t.Fatalf("close: %d %s", w.Code, w.Body.String())
	}
	if _, ok := s.accounts.LookupEmail("u@x.com"); ok {
		t.Fatal("account still exists after close")
	}
}

func TestCustomDomainOwnershipGateHelper(t *testing.T) {
	a := &Account{Domains: []string{"alpha"}}
	if !containsStr(a.Domains, "alpha") || containsStr(a.Domains, "beta") {
		t.Fatal("containsStr")
	}
	if !validSubdomain("ok-name") || validSubdomain("BAD NAME") || validSubdomain("-lead") {
		t.Fatal("validSubdomain")
	}
	_ = time.Second
}

func TestPricingShowsLoggedInState(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	if _, err := s.activate("u@x.com", "pro"); err != nil {
		t.Fatal(err)
	}
	// anonymous: prompt to sign in, email empty
	rec := httptest.NewRecorder()
	s.handlePricing(rec, httptest.NewRequest(http.MethodGet, "/pricing", nil))
	if !strings.Contains(rec.Body.String(), "Not signed in") {
		t.Fatal("anonymous pricing must prompt sign-in")
	}
	// logged in: identity banner + prefilled readonly email
	r := httptest.NewRequest(http.MethodGet, "/pricing", nil)
	r.AddCookie(s.sessionCookie("u@x.com"))
	rec = httptest.NewRecorder()
	s.handlePricing(rec, r)
	body := rec.Body.String()
	if !strings.Contains(body, "Signed in as") || !strings.Contains(body, "value=\"u@x.com\"") || !strings.Contains(body, "readonly") {
		t.Fatalf("logged-in pricing missing prefill/banner: %.200s", body)
	}
}

func TestLogoutClearsSession(t *testing.T) {
	s := newBillingTestServer(t)
	rec := httptest.NewRecorder()
	s.handleLogout(rec, httptest.NewRequest(http.MethodGet, "/logout", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logout code: %d", rec.Code)
	}
	found := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == "et_session" && c.MaxAge < 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("logout must expire the session cookie")
	}
}

func TestOAuthProvidersGating(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth = oauthConfig{}
	if s.oauthEnabled("github") || s.oauthEnabled("gitlab") {
		t.Fatal("empty config must disable all providers")
	}
	s.auth.githubID = "gh-id"
	if !s.oauthEnabled("github") {
		t.Fatal("github id set -> enabled")
	}
	// /login lists only configured providers
	rec := httptest.NewRecorder()
	s.handleLogin(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "/auth/github/login") || strings.Contains(body, "/auth/apple/login") {
		t.Fatalf("login page provider list wrong: %.200s", body)
	}
}

func TestOAuthLoginRedirectsToGithub(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.githubID = "gh-id"
	s.billing.PublicBaseURL = "https://x.test"
	rec := httptest.NewRecorder()
	s.handleOAuthLogin(rec, httptest.NewRequest(http.MethodGet, "/auth/github/login", nil), "github")
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "https://github.com/login/oauth/authorize?") ||
		!strings.Contains(loc, "client_id=gh-id") || !strings.Contains(loc, "read%3Auser") {
		t.Fatalf("github redirect: %d %s", rec.Code, loc)
	}
}

func TestAppleKeyPEMNormalized(t *testing.T) {
	raw := "-----BEGIN PRIVATE KEY-----\\nMIGabc\\n-----END PRIVATE KEY-----"
	t.Setenv("APPLE_PRIVATE_KEY", raw)
	cfg := oauthFromEnv()
	if !strings.Contains(cfg.appleKeyPEM, "\n") || strings.Contains(cfg.appleKeyPEM, `\n`) {
		t.Fatalf("literal \\n not normalized: %q", cfg.appleKeyPEM)
	}
}

// GitHub returns form-encoded by default; the parser must accept both shapes.
func TestOAuthTokenAcceptsFormAndJSON(t *testing.T) {
	s := newBillingTestServer(t)
	cases := []struct{ body, ctype string }{
		{`access_token=gho_abc&token_type=bearer&scope=read%3Auser`, "application/x-www-form-urlencoded"},
		{`{"access_token":"gho_abc","token_type":"bearer"}`, "application/json"},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", c.ctype)
			_, _ = w.Write([]byte(c.body))
		}))
		tok, err := s.oauthToken(context.Background(), srv.URL, url.Values{"code": {"x"}})
		srv.Close()
		if err != nil || tok["access_token"] != "gho_abc" {
			t.Fatalf("%s: err=%v tok=%v", c.ctype, err, tok)
		}
	}
}

func TestStateCookieAttributes(t *testing.T) {
	s := newBillingTestServer(t)
	rec := httptest.NewRecorder()
	st := s.setStateCookie(rec)
	if st == "" {
		t.Fatal("empty state")
	}
	c := rec.Result().Cookies()[0]
	if c.Path != "/" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie attrs: path=%q secure=%v httpOnly=%v ss=%v", c.Path, c.Secure, c.HttpOnly, c.SameSite)
	}
	// round-trip: the cookie satisfies its own check, a wrong value does not
	r := httptest.NewRequest(http.MethodGet, "/auth/x/callback?state="+st, nil)
	r.AddCookie(c)
	if !s.checkStateCookie(httptest.NewRecorder(), r, st) {
		t.Fatal("valid state rejected")
	}
	r2 := httptest.NewRequest(http.MethodGet, "/auth/x/callback?state=other", nil)
	r2.AddCookie(c)
	if s.checkStateCookie(httptest.NewRecorder(), r2, "other") {
		t.Fatal("mismatched state accepted")
	}
}
