package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// cliTestEnv spins a Server with one account and returns it plus the account
// email and a valid session cookie for that account.
func cliTestEnv(t *testing.T) (*Server, string, *http.Cookie) {
	t.Helper()
	s, err := New(Config{Domain: "t.test"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	acc, err := s.accounts.(*AccountStore).Create("cli-user@example.com", "free")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	return s, acc.Email, s.sessionCookie(acc.Email)
}

func cliPost[T any](t *testing.T, s *Server, path string, body any, cookie *http.Cookie) (int, T) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(b)))
	req.Host = s.cfg.Domain // routeByHost: base-domain endpoints need the service host
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	s.serveCLI(rec, req)
	var out T
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// TestCLIFlowE2E walks start -> grant (GET approval page, POST allow) ->
// loopback redirect -> exchange, and checks the credentials payload.
func TestCLIFlowE2E(t *testing.T) {
	s, email, cookie := cliTestEnv(t)

	// start
	code, start := cliPost[struct {
		AuthURL    string `json:"auth_url"`
		DeviceCode string `json:"device_code"`
	}](t, s, "/auth/cli/start", map[string]string{"redirect_uri": "http://127.0.0.1:45678/callback"}, nil)
	if code != http.StatusOK || start.DeviceCode == "" || !strings.Contains(start.AuthURL, "/auth/cli/grant?dc=") {
		t.Fatalf("start: %d %+v", code, start)
	}

	// grant GET: approval page
	req := httptest.NewRequest(http.MethodGet, "/auth/cli/grant?dc="+start.DeviceCode, nil)
	req.Host = s.cfg.Domain
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.serveCLI(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Authorize command-line access") {
		t.Fatalf("grant GET: %d body=%q", rec.Code, rec.Body.String()[:min(200, rec.Body.Len())])
	}

	// grant POST: allow -> 303 to the loopback redirect_uri with the code
	req = httptest.NewRequest(http.MethodPost, "/auth/cli/grant?dc="+start.DeviceCode, strings.NewReader("action=allow"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = s.cfg.Domain
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	s.serveCLI(rec, req)
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "http://127.0.0.1:45678/callback?code=") {
		t.Fatalf("grant POST: %d loc=%q", rec.Code, loc)
	}
	authCode := strings.TrimPrefix(loc, "http://127.0.0.1:45678/callback?code=")

	// exchange
	code, creds := cliPost[struct {
		APIToken   string   `json:"api_token"`
		Subdomains []string `json:"subdomains"`
		Plan       string   `json:"plan"`
		ExpiresAt  int64    `json:"expires_at"`
	}](t, s, "/auth/cli/exchange", map[string]string{"device_code": start.DeviceCode, "code": authCode}, nil)
	if code != http.StatusOK {
		t.Fatalf("exchange: %d body=%s", code, rec.Body.String())
	}
	acc, _ := s.accounts.(*AccountStore).LookupEmail(email)
	if creds.APIToken != acc.APIKey || creds.Plan != "free" || creds.ExpiresAt != 0 {
		t.Fatalf("credentials mismatch: %+v", creds)
	}
}

// TestCLIExchangeCodeReplay proves the code is one-time: a second exchange
// with the same pair is rejected.
func TestCLIExchangeCodeReplay(t *testing.T) {
	s, _, cookie := cliTestEnv(t)
	_, start := cliPost[struct {
		DeviceCode string `json:"device_code"`
	}](t, s, "/auth/cli/start", map[string]string{"redirect_uri": "http://127.0.0.1:1/callback"}, nil)

	req := httptest.NewRequest(http.MethodPost, "/auth/cli/grant?dc="+start.DeviceCode, strings.NewReader("action=allow"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = s.cfg.Domain
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.serveCLI(rec, req)
	authCode := strings.TrimPrefix(rec.Header().Get("Location"), "http://127.0.0.1:1/callback?code=")

	body := map[string]string{"device_code": start.DeviceCode, "code": authCode}
	if c, _ := cliPost[struct{}](t, s, "/auth/cli/exchange", body, nil); c != http.StatusOK {
		t.Fatalf("first exchange should succeed, got %d", c)
	}
	if c, _ := cliPost[struct{}](t, s, "/auth/cli/exchange", body, nil); c != http.StatusBadRequest {
		t.Fatalf("replay should be rejected, got %d", c)
	}
}

// TestCLICrossFlowPairing proves a code minted for flow A cannot be redeemed
// with flow B's device code.
func TestCLICrossFlowPairing(t *testing.T) {
	s, _, cookie := cliTestEnv(t)
	_, a := cliPost[struct {
		DeviceCode string `json:"device_code"`
	}](t, s, "/auth/cli/start", map[string]string{"redirect_uri": "http://127.0.0.1:1/callback"}, nil)
	_, b := cliPost[struct {
		DeviceCode string `json:"device_code"`
	}](t, s, "/auth/cli/start", map[string]string{"redirect_uri": "http://127.0.0.1:2/callback"}, nil)

	req := httptest.NewRequest(http.MethodPost, "/auth/cli/grant?dc="+a.DeviceCode, strings.NewReader("action=allow"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = s.cfg.Domain
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.serveCLI(rec, req)
	authCode := strings.TrimPrefix(rec.Header().Get("Location"), "http://127.0.0.1:1/callback?code=")

	// Redeem with B's device code: pairing must fail.
	if c, _ := cliPost[struct{}](t, s, "/auth/cli/exchange", map[string]string{"device_code": b.DeviceCode, "code": authCode}, nil); c != http.StatusBadRequest {
		t.Fatalf("cross-flow exchange should be rejected, got %d", c)
	}
}

// TestCLIExpiredCode mints a code with a past expiry (via the internal signer)
// and proves the exchange rejects it.
func TestCLIExpiredCode(t *testing.T) {
	s, _, cookie := cliTestEnv(t)
	_, start := cliPost[struct {
		DeviceCode string `json:"device_code"`
	}](t, s, "/auth/cli/start", map[string]string{"redirect_uri": "http://127.0.0.1:1/callback"}, nil)

	// Forge an already-expired code for this flow through the real signer:
	// proves the exp check, not just the HMAC.
	var dev cliDevice
	payload, _ := s.cliVerify("dev", start.DeviceCode)
	_ = json.Unmarshal([]byte(payload), &dev)
	cp, _ := json.Marshal(cliCode{Email: "cli-user@example.com", Nonce: dev.Nonce, Exp: time.Now().Add(-time.Minute).Unix()})
	expired := s.cliSign("code", string(cp))

	if c, _ := cliPost[struct{}](t, s, "/auth/cli/exchange", map[string]string{"device_code": start.DeviceCode, "code": expired}, nil); c != http.StatusBadRequest {
		t.Fatalf("expired code should be rejected, got %d", c)
	}
	_ = cookie
}

// TestCLIStartRejectsNonLoopback locks down the redirect_uri shape: open
// redirects would leak the code to an attacker-controlled host.
func TestCLIStartRejectsNonLoopback(t *testing.T) {
	s, _, _ := cliTestEnv(t)
	for _, uri := range []string{
		"https://127.0.0.1:1/callback",    // must be plain http
		"http://evil.com/callback",        // not loopback
		"http://127.0.0.1:1/other",        // wrong path
		"http://127.0.0.1:1/callback?x=1", // no query allowed
		"http://127.0.0.1/callback",       // port required
	} {
		if c, _ := cliPost[struct{}](t, s, "/auth/cli/start", map[string]string{"redirect_uri": uri}, nil); c != http.StatusBadRequest {
			t.Fatalf("redirect_uri %q should be rejected, got %d", uri, c)
		}
	}
}

// TestCLIGrantRequiresSession proves the approval page demands a login.
func TestCLIGrantRequiresSession(t *testing.T) {
	s, _, _ := cliTestEnv(t)
	_, start := cliPost[struct {
		DeviceCode string `json:"device_code"`
	}](t, s, "/auth/cli/start", map[string]string{"redirect_uri": "http://127.0.0.1:1/callback"}, nil)
	req := httptest.NewRequest(http.MethodGet, "/auth/cli/grant?dc="+start.DeviceCode, nil)
	req.Host = s.cfg.Domain
	rec := httptest.NewRecorder()
	s.serveCLI(rec, req)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login?return_to=") {
		t.Fatalf("unauthenticated grant should redirect to /login with return_to, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// TestCLICredsPayloadFreeTier pins the free-tier shape the SDKs code against.
func TestCLICredsPayloadFreeTier(t *testing.T) {
	s, email, cookie := cliTestEnv(t)
	_, start := cliPost[struct {
		DeviceCode string `json:"device_code"`
	}](t, s, "/auth/cli/start", map[string]string{"redirect_uri": "http://127.0.0.1:1/callback"}, nil)
	req := httptest.NewRequest(http.MethodPost, "/auth/cli/grant?dc="+start.DeviceCode, strings.NewReader("action=allow"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = s.cfg.Domain
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.serveCLI(rec, req)
	authCode := strings.TrimPrefix(rec.Header().Get("Location"), "http://127.0.0.1:1/callback?code=")

	req2 := httptest.NewRequest(http.MethodPost, "/auth/cli/exchange", strings.NewReader(`{"device_code":"`+start.DeviceCode+`","code":"`+authCode+`"}`))
	req2.Host = s.cfg.Domain
	rec2 := httptest.NewRecorder()
	s.serveCLI(rec2, req2)
	var raw map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"api_token", "subdomains", "plan", "expires_at"} {
		if _, ok := raw[k]; !ok {
			t.Fatalf("payload missing %q: %s", k, rec2.Body.String())
		}
	}
	// free tier: subdomains serializes as JSON null - the key must be present
	// (it is, above) but carries no domains.
	_ = email
	_ = io.Discard
}

func (s *Server) serveCLI(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }
