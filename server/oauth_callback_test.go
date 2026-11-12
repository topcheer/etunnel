package server

// #17: the OAuth callback's business rules (find-or-create by provider
// subject; email-claim with key rotation; plain create) had no end-to-end
// fence. These drive the real handler through a fake GitLab (gitlabURL is
// injectable without touching product code).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func newFakeGitlab(t *testing.T, id int64, email, name string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-1"})
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": id, "username": name, "email": email, "name": name,
		})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func oauthLoginFlow(t *testing.T, s *Server, id int64, email string) *httptest.ResponseRecorder {
	t.Helper()
	fake := newFakeGitlab(t, id, email, "Test User")
	s.auth.gitlabID, s.auth.gitlabSecret, s.auth.gitlabURL = "id1", "sec1", fake.URL
	s.auth.sessionSecret = "sess"
	_ = fake

	// 1) /login/gitlab seeds the state cookie and 302s to the fake provider.
	req := httptest.NewRequest(http.MethodGet, "/login/gitlab", nil)
	req.Host = "t.test"
	loginRec := httptest.NewRecorder()
	s.handleOAuthLogin(loginRec, req, "gitlab")
	state := loginStateFromLocation(t, loginRec.Header().Get("Location"))
	cookie := loginRec.Header().Get("Set-Cookie")

	// 2) callback with the matching state cookie.
	req2 := httptest.NewRequest(http.MethodGet, "/auth/gitlab/callback?code=c1&state="+state, nil)
	req2.Host = "t.test"
	req2.Header.Set("Cookie", cookie)
	rec := httptest.NewRecorder()
	s.handleOAuthCallback(rec, req2, "gitlab")
	return rec
}

func loginStateFromLocation(t *testing.T, loc string) string {
	t.Helper()
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

func TestOAuthCallbackCreatesAccount(t *testing.T) {
	s := newBillingTestServer(t)
	rec := oauthLoginFlow(t, s, 101, "New@Example.com")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303 to /me, got %d %s", rec.Code, rec.Body.String())
	}
	a, ok := s.accounts.LookupEmail("new@example.com")
	if !ok || a.Provider != "gitlab" || a.Subject != "101" {
		t.Fatalf("account not created/linked: %+v", a)
	}
}

func TestOAuthCallbackClaimsExistingAccountAndRotatesKey(t *testing.T) {
	s := newBillingTestServer(t)
	// anonymous pre-registration squats the email
	created, err := s.accounts.Create("held@example.com", "free")
	if err != nil {
		t.Fatal(err)
	}
	oldKey := created.APIKey

	rec := oauthLoginFlow(t, s, 199, "held@example.com")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("claim failed: %d %s", rec.Code, rec.Body.String())
	}
	a, _ := s.accounts.LookupEmail("held@example.com")
	if a.Provider != "gitlab" || a.Subject != "199" {
		t.Fatalf("claim did not link identity: %+v", a)
	}
	if a.APIKey == oldKey {
		t.Fatal("pre-squatting provider key still valid after claim; key rotation missing")
	}
}

func TestOAuthCallbackRefindsBySubject(t *testing.T) {
	s := newBillingTestServer(t)
	oauthLoginFlow(t, s, 107, "First@Example.com")
	// second login, same subject, different email casing: provider hit, no duplicate
	rec := oauthLoginFlow(t, s, 107, "FIRST@example.com")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("relogin failed: %d", rec.Code)
	}
	if got := len(s.accounts.List()); got != 1 {
		t.Fatalf("provider re-hit must not duplicate the account: %d accounts", got)
	}
}
