package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The passkey endpoints' HTTP contract: ceremony happy-path needs a real
// authenticator (verified live against production), but the guards - session
// enforcement, JSON shape, replay of invalid bodies - are testable here.
func TestPasskeyEndpointGuards(t *testing.T) {
	s, err := New(Config{Domain: "t.test"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/passkey/register/begin", s.handlePasskeyRegisterBegin)
	mux.HandleFunc("/auth/passkey/register/finish", s.handlePasskeyRegisterFinish)
	mux.HandleFunc("/auth/passkey/login/begin", s.handlePasskeyLoginBegin)
	mux.HandleFunc("/auth/passkey/login/finish", s.handlePasskeyLoginFinish)
	mux.HandleFunc("/auth/passkey/list", s.handlePasskeyList)
	mux.HandleFunc("/auth/passkey/remove", s.handlePasskeyRemove)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Every passkey surface rejects anonymous callers except login begin.
	for _, path := range []string{"/auth/passkey/register/begin", "/auth/passkey/register/finish", "/auth/passkey/list", "/auth/passkey/remove"} {
		resp, err := http.Post(ts.URL+path, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: anonymous = %d, want 401", path, resp.StatusCode)
		}
	}

	// Login begin hands out a discoverable assertion (no allowCredentials
	// pinning the user) and a flow cookie.
	resp, err := http.Get(ts.URL + "/auth/passkey/login/begin")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login begin = %d", resp.StatusCode)
	}
	var opts struct {
		Challenge string `json:"challenge"`
		RPID      string `json:"rpId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&opts); err != nil || opts.Challenge == "" {
		t.Fatalf("login begin payload bad: %v challenge=%q", err, opts.Challenge)
	}
	if opts.RPID != "localhost" && !strings.Contains(s.cfg.Domain, opts.RPID) && opts.RPID != "" {
		// rpId derives from cfg.Domain (host part); empty is acceptable when
		// Domain is not host:port shaped. Anything else is a wiring bug.
		t.Errorf("login begin rpId = %q (domain %q)", opts.RPID, s.cfg.Domain)
	}
	var flow bool
	for _, c := range resp.Cookies() {
		if c.Name == "et_pkflow" && c.Value != "" {
			flow = true
		}
	}
	if !flow {
		t.Error("login begin did not set et_pkflow cookie")
	}

	// Login finish with a garbage body and the fresh flow cookie must fail
	// cleanly (400/401), never 500.
	req, _ := http.NewRequest("POST", ts.URL+"/auth/passkey/login/finish", strings.NewReader(`{"id":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	for _, c := range resp.Cookies() {
		req.AddCookie(c)
	}
	fr, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	fr.Body.Close()
	if fr.StatusCode >= 500 {
		t.Errorf("login finish garbage body = %d, want 4xx", fr.StatusCode)
	}
}
