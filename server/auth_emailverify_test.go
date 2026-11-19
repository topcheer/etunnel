package server

// Regression tests for #215: OAuth identity emails must only be trusted
// when the provider has verified them (or synthesized a per-subject noreply
// address). An unverified email must never claim an existing account
// (no key rotation, no identity rebinding) and must not create one either.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// stubTransport serves canned JSON bodies keyed by URL substring so the
// identity tests run without network access.
type stubTransport struct{ bySub map[string]string }

func (st stubTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// Longest key first: "/api/v4/user" is a substring of
	// "/api/v4/user/emails", so map-iteration order alone would flake.
	keys := make([]string, 0, len(st.bySub))
	for k := range st.bySub {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, sub := range keys {
		if strings.Contains(r.URL.String(), sub) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(st.bySub[sub])),
				Request:    r,
			}, nil
		}
	}
	return nil, fmt.Errorf("etunnel: no oauth stub for %s", r.URL)
}

func withStubOAuth(t *testing.T, bySub map[string]string) {
	t.Helper()
	old := oauthHTTP
	oauthHTTP = &http.Client{Transport: stubTransport{bySub}}
	t.Cleanup(func() { oauthHTTP = old })
}

// testJWT builds an unverified (but well-formed) id_token; the Apple branch
// only reads claims after the token endpoint exchange, which is stubbed.
func testJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(map[string]string{"alg": "none"})
	c, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString(h) + "." +
		base64.RawURLEncoding.EncodeToString(c) + ".sig"
}

func newOauthTestServer(t *testing.T, gitlabURL string) *Server {
	t.Helper()
	store, err := NewAccountStore("")
	if err != nil {
		t.Fatalf("account store: %v", err)
	}
	s := &Server{accounts: store}
	s.cfg.Domain = "t.test"
	s.billing.PublicBaseURL = "https://pub.example"
	s.auth.gitlabID, s.auth.gitlabSecret = "id1", "sec1"
	s.auth.gitlabURL = gitlabURL
	return s
}

func TestOAuthIdentityVerifiedParsing(t *testing.T) {
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("gitlab should be stubbed, hit %s", r.URL)
	}))
	t.Cleanup(gl.Close)

	cases := []struct {
		name     string
		provider string
		stubs    map[string]string
		gitlab   string // overrides s.auth.gitlabURL
		want     oauthIdentityRes
	}{
		{
			name:     "google verified email",
			provider: "google",
			stubs: map[string]string{
				"oauth2.googleapis.com/token":  `{"access_token":"t"}`,
				"openidconnect.googleapis.com": `{"sub":"g1","email":"A@x.com","email_verified":true,"name":"A"}`,
			},
			want: oauthIdentityRes{subject: "g1", email: "a@x.com", name: "A", emailVerified: true},
		},
		{
			name:     "google unverified email",
			provider: "google",
			stubs: map[string]string{
				"oauth2.googleapis.com/token":  `{"access_token":"t"}`,
				"openidconnect.googleapis.com": `{"sub":"g2","email":"a@x.com","email_verified":false,"name":"A"}`,
			},
			want: oauthIdentityRes{subject: "g2", email: "a@x.com", name: "A", emailVerified: false},
		},
		{
			name:     "gitlab confirmed email",
			provider: "gitlab",
			gitlab:   gl.URL,
			stubs: map[string]string{
				gl.URL + "/oauth/token":        `{"access_token":"t"}`,
				gl.URL + "/api/v4/user":        `{"id":9,"username":"eve","email":"Eve@x.com","name":"E"}`,
				gl.URL + "/api/v4/user/emails": `[{"email":"eve@x.com","confirmed_at":"2024-01-01T00:00:00Z"}]`,
			},
			want: oauthIdentityRes{subject: "9", email: "eve@x.com", name: "E", emailVerified: true},
		},
		{
			name:     "gitlab unconfirmed email fails closed",
			provider: "gitlab",
			gitlab:   gl.URL,
			stubs: map[string]string{
				gl.URL + "/oauth/token":        `{"access_token":"t"}`,
				gl.URL + "/api/v4/user":        `{"id":9,"username":"eve","email":"eve@x.com","name":"E"}`,
				gl.URL + "/api/v4/user/emails": `[{"email":"eve@x.com","confirmed_at":null}]`,
			},
			want: oauthIdentityRes{subject: "9", email: "eve@x.com", name: "E", emailVerified: false},
		},
		{
			name:     "gitlab no email synthesizes noreply",
			provider: "gitlab",
			gitlab:   gl.URL,
			stubs: map[string]string{
				gl.URL + "/oauth/token": `{"access_token":"t"}`,
				gl.URL + "/api/v4/user": `{"id":11,"username":"bob","email":"","name":"B"}`,
			},
			want: oauthIdentityRes{subject: "11", email: "bob@users.noreply.gitlab.com", name: "B", emailVerified: true},
		},
		{
			name:     "github trusted",
			provider: "github",
			stubs: map[string]string{
				"github.com/login/oauth/access_token": `{"access_token":"t"}`,
				"api.github.com/user":                 `{"id":7,"login":"dev","name":"D","email":"dev@x.com"}`,
			},
			want: oauthIdentityRes{subject: "7", email: "dev@x.com", name: "D", emailVerified: true},
		},
		{
			name:     "apple first login verified string",
			provider: "apple",
			stubs: map[string]string{
				"appleid.apple.com/auth/token": `{"id_token":"JWT"}`,
			},
			want: oauthIdentityRes{subject: "a1", email: "a@x.com", emailVerified: true},
		},
		{
			name:     "apple unverified string rejected",
			provider: "apple",
			stubs: map[string]string{
				"appleid.apple.com/auth/token": `{"id_token":"JWT"}`,
			},
			want: oauthIdentityRes{subject: "a2", email: "a@x.com", emailVerified: false},
		},
		{
			name:     "apple returning user no email gets noreply",
			provider: "apple",
			stubs: map[string]string{
				"appleid.apple.com/auth/token": `{"id_token":"JWT"}`,
			},
			want: oauthIdentityRes{subject: "a3", email: "a3@users.noreply.apple.com", emailVerified: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubs := map[string]string{}
			for k, v := range tc.stubs {
				stubs[k] = v
			}
			// per-case JWT for apple
			if tc.provider == "apple" {
				claims := map[string]any{"sub": tc.want.subject}
				switch tc.want.emailVerified {
				case true:
					if strings.Contains(tc.want.email, "noreply") {
						// returning user: no email at all
					} else {
						claims["email"], claims["email_verified"] = tc.want.email, "true"
					}
				case false:
					claims["email"], claims["email_verified"] = tc.want.email, "false"
				}
				stubs["appleid.apple.com/auth/token"] = `{"id_token":"` + testJWT(t, claims) + `"}`
			}
			withStubOAuth(t, stubs)
			s := &Server{}
			s.billing.PublicBaseURL = "https://pub.example"
			if tc.gitlab != "" {
				s.auth.gitlabURL = tc.gitlab
			}
			id, err := s.oauthIdentity(context.Background(), tc.provider, "code")
			if err != nil {
				t.Fatalf("identity: %v", err)
			}
			if *id != tc.want {
				t.Fatalf("got %+v want %+v", *id, tc.want)
			}
		})
	}
}

// The core #215 takeover: an unverified email that matches an existing
// account must be rejected before the claim branch - no key rotation, no
// provider/subject rebinding, no session cookie.
func TestOAuthCallbackRejectsUnverifiedEmailClaim(t *testing.T) {
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("gitlab should be stubbed, hit %s", r.URL)
	}))
	t.Cleanup(gl.Close)
	withStubOAuth(t, map[string]string{
		gl.URL + "/oauth/token":        `{"access_token":"t"}`,
		gl.URL + "/api/v4/user":        `{"id":9,"username":"eve","email":"victim@x.com","name":"Eve"}`,
		gl.URL + "/api/v4/user/emails": `[{"email":"victim@x.com","confirmed_at":null}]`,
	})
	s := newOauthTestServer(t, gl.URL)
	victim, err := s.accounts.Create("victim@x.com", "paid")
	if err != nil {
		t.Fatalf("create victim: %v", err)
	}
	keyBefore := victim.APIKey

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/auth/gitlab/callback",
		strings.NewReader("code=c&state=st"))
	r.Host = "t.test"
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "et_oauth_state", Value: "st"})
	s.handleOAuthCallback(w, r, "gitlab")

	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d (body %q)", w.Code, w.Body.String())
	}
	after, ok := s.accounts.LookupEmail("victim@x.com")
	if !ok {
		t.Fatal("victim account vanished")
	}
	if after.APIKey != keyBefore {
		t.Fatal("API key was rotated on a REJECTED login - takeover side effect")
	}
	if after.Provider != "" || after.Subject != "" {
		t.Fatalf("identity rebound on rejected login: provider=%q subject=%q", after.Provider, after.Subject)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == "et_session" {
			t.Fatal("session cookie issued on rejected login")
		}
	}
}

// An unverified email that matches NO existing account must not create one.
func TestOAuthCallbackUnverifiedNewEmailCreatesNothing(t *testing.T) {
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("gitlab should be stubbed, hit %s", r.URL)
	}))
	t.Cleanup(gl.Close)
	withStubOAuth(t, map[string]string{
		gl.URL + "/oauth/token":        `{"access_token":"t"}`,
		gl.URL + "/api/v4/user":        `{"id":9,"username":"eve","email":"fresh@x.com","name":"Eve"}`,
		gl.URL + "/api/v4/user/emails": `[{"email":"fresh@x.com","confirmed_at":null}]`,
	})
	s := newOauthTestServer(t, gl.URL)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/auth/gitlab/callback",
		strings.NewReader("code=c&state=st"))
	r.Host = "t.test"
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "et_oauth_state", Value: "st"})
	s.handleOAuthCallback(w, r, "gitlab")

	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}
	if _, ok := s.accounts.LookupEmail("fresh@x.com"); ok {
		t.Fatal("account created from an unverified email")
	}
}

// Apple omits the email on every login after the first authorization;
// returning users must still log in (via provider+subject) instead of
// being locked out by the verified gate.
func TestOAuthCallbackAppleReturningUserAllowed(t *testing.T) {
	jwt := testJWT(t, map[string]any{"sub": "a9"})
	withStubOAuth(t, map[string]string{
		"appleid.apple.com/auth/token": `{"id_token":"` + jwt + `"}`,
	})
	s := newOauthTestServer(t, "")
	s.auth.appleID, s.auth.appleTeam, s.auth.appleKey = "aid", "team", "kid"
	s.auth.appleKeyPEM = "not-a-real-pem" // oauthEnabled gate only; stub ignores the secret

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/auth/apple/callback",
		strings.NewReader("code=c&state=st"))
	r.Host = "t.test"
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "et_oauth_state", Value: "st"})
	s.handleOAuthCallback(w, r, "apple")

	if w.Code == http.StatusForbidden {
		t.Fatal("returning Apple user rejected by the verified gate")
	}
	if _, ok := s.accounts.LookupEmail("a9@users.noreply.apple.com"); !ok {
		t.Fatalf("noreply account missing (status %d)", w.Code)
	}
}
