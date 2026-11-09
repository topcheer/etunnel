package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Issue #17: the OAuth callback's find-or-create and email-claim branches had
// no end-to-end coverage. GitLab is the provider whose endpoints are
// configurable, so a fake provider can drive the real handler.

func fakeGitLab(t *testing.T, subject int64, email, name string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if r.PostFormValue("code") == "" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok-123","token_type":"bearer"}`))
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, `{"message":"401 Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":` + strconv.FormatInt(subject, 10) + `,"username":"octo","email":"` + email + `","name":"` + name + `"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newOAuthTestServer(t *testing.T, gitlabURL string) *Server {
	t.Helper()
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "testsecret"
	s.auth.gitlabID = "client-id"
	s.auth.gitlabSecret = "client-secret"
	s.auth.gitlabURL = gitlabURL
	s.billing.PublicBaseURL = "https://t.test"
	return s
}

func doCallback(t *testing.T, s *Server, code, state string) *httptest.ResponseRecorder {
	t.Helper()
	u := "/auth/gitlab/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state)
	r := httptest.NewRequest(http.MethodGet, u, nil)
	r.AddCookie(&http.Cookie{Name: "et_oauth_state", Value: state})
	w := httptest.NewRecorder()
	s.handleOAuthCallback(w, r, "gitlab")
	return w
}

func TestOAuthCallbackCreatesAccount(t *testing.T) {
	fake := fakeGitLab(t, 4242, "Octo@Example.COM", "Octo Cat")
	s := newOAuthTestServer(t, fake.URL)

	w := doCallback(t, s, "good-code", "state-abc")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/me" {
		t.Fatalf("status=%d location=%q body=%s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	if sc := w.Result().Cookies(); len(sc) == 0 {
		t.Fatal("no session cookie issued")
	}

	acct, ok := s.accounts.LookupEmail("octo@example.com")
	if !ok {
		t.Fatal("account not created (email should be lowercased)")
	}
	if acct.Tier != "free" || acct.Provider != "gitlab" || acct.Subject != "4242" || acct.Name != "Octo Cat" {
		t.Fatalf("unexpected account: %+v", acct)
	}
}

// The email-claim branch: an anonymous account pre-registered with the same
// address is claimed, and its API key rotates (#2) so squatters keep nothing.
func TestOAuthCallbackClaimsExistingAccountAndRotatesKey(t *testing.T) {
	fake := fakeGitLab(t, 777, "squat@x.com", "Squatter")
	s := newOAuthTestServer(t, fake.URL)

	first, err := s.accounts.Create("squat@x.com", "free")
	if err != nil {
		t.Fatal(err)
	}
	oldKey := first.APIKey

	w := doCallback(t, s, "good-code", "state-xyz")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if _, ok := s.accounts.Lookup(oldKey); ok {
		t.Fatal("pre-existing key still valid after claim; key rotation missing")
	}
	acct, ok := s.accounts.LookupEmail("squat@x.com")
	if !ok {
		t.Fatal("account disappeared")
	}
	if acct.Provider != "gitlab" || acct.Subject != "777" {
		t.Fatalf("account not claimed: %+v", acct)
	}
	if acct.APIKey == oldKey {
		t.Fatal("API key not rotated on claim")
	}
}

func TestOAuthCallbackRejectsBadState(t *testing.T) {
	fake := fakeGitLab(t, 1, "a@x.com", "A")
	s := newOAuthTestServer(t, fake.URL)

	r := httptest.NewRequest(http.MethodGet, "/auth/gitlab/callback?code=c&state=query-state", nil)
	r.AddCookie(&http.Cookie{Name: "et_oauth_state", Value: "cookie-state"})
	w := httptest.NewRecorder()
	s.handleOAuthCallback(w, r, "gitlab")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", w.Code)
	}
	if _, ok := s.accounts.LookupEmail("a@x.com"); ok {
		t.Fatal("account created despite state mismatch")
	}
}

func TestOAuthCallbackSurfacesProviderFailure(t *testing.T) {
	s := newOAuthTestServer(t, "http://127.0.0.1:1") // nothing listens
	w := doCallback(t, s, "good-code", "state-abc")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502", w.Code)
	}
}

// Issue #17/#4: a paid activation racing the handshake's snapshot read must not
// tear. Runs under -race.
func TestActivateConcurrentWithSnapshot(t *testing.T) {
	s := newBillingTestServer(t)
	acct, err := s.accounts.Create("race@x.com", "free")
	if err != nil {
		t.Fatal(err)
	}
	key := acct.APIKey

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() { // writer: tier flips + domain churn, as payment/admin actions do
		defer wg.Done()
		for i := 0; i < 200; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = s.accounts.Mutate("race@x.com", func(a *Account) {
				if i%2 == 0 {
					a.Tier = "pro"
					a.Domains = []string{"a", "b"}
				} else {
					a.Tier = "free"
					a.Domains = nil
				}
			})
		}
	}()

	wg.Add(1)
	go func() { // reader: exactly what the handshake does
		defer wg.Done()
		defer close(stop)
		for i := 0; i < 200; i++ {
			snap, ok := s.accounts.Snapshot(key)
			if !ok {
				return
			}
			_ = snap.Tier
			_ = strings.Join(snap.Domains, ",")
		}
	}()

	wg.Wait()
}
