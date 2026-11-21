package server

// #293: /me actions and passkey removal are state changes issued from the
// portal. Tunnel subdomains are same-site with the portal domain (so the
// session cookie's SameSite=Lax does not protect them), and text/plain is a
// CORS simple request (no preflight, cookie-bearing). The browser-supplied
// Origin/Referer must therefore name the portal base domain itself, a
// request with neither header is rejected, and the body must be
// application/json.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func mePost(s *Server, email, action, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/me/"+action, bytes.NewBufferString(body))
	r.AddCookie(s.sessionCookie(email))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.handleMeAction(w, r, action)
	return w
}

func TestMeActionRejectsCrossOrigin(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	if _, err := s.activate("u@x.com", "pro"); err != nil {
		t.Fatal(err)
	}
	const own = `{"email":"u@x.com"}`

	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"tunnel subdomain origin", map[string]string{
			"Origin":       "https://attacker.t.test",
			"Content-Type": "application/json"}, http.StatusForbidden},
		{"tunnel subdomain referer only", map[string]string{
			"Referer":      "https://attacker.t.test/phish",
			"Content-Type": "application/json"}, http.StatusForbidden},
		{"no origin and no referer", map[string]string{
			"Content-Type": "application/json"}, http.StatusForbidden},
		{"unrelated origin", map[string]string{
			"Origin":       "https://evil.example",
			"Content-Type": "application/json"}, http.StatusForbidden},
		{"portal origin", map[string]string{
			"Origin":       "https://" + s.cfg.Domain,
			"Content-Type": "application/json"}, http.StatusOK},
		{"portal referer only", map[string]string{
			"Referer":      "https://" + s.cfg.Domain + "/me",
			"Content-Type": "application/json"}, http.StatusOK},
		{"text/plain simple request", map[string]string{
			"Origin":       "https://" + s.cfg.Domain,
			"Content-Type": "text/plain"}, http.StatusBadRequest},
		{"missing content type", map[string]string{
			"Origin": "https://" + s.cfg.Domain}, http.StatusBadRequest},
	}
	for _, c := range cases {
		w := mePost(s, "u@x.com", "reset", own, c.headers)
		if w.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.name, w.Code, c.want)
		}
	}
}

func TestPasskeyRemoveSameOrigin(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	if _, err := s.activate("p@x.com", "pro"); err != nil {
		t.Fatal(err)
	}

	call := func(id string, headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/auth/passkey/remove?id="+id, nil)
		r.AddCookie(s.sessionCookie("p@x.com"))
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		s.handlePasskeyRemove(w, r)
		return w
	}

	if w := call("AA", map[string]string{"Origin": "https://attacker.t.test"}); w.Code != http.StatusForbidden {
		t.Errorf("tunnel origin: %d, want 403", w.Code)
	}
	if w := call("AA", nil); w.Code != http.StatusForbidden {
		t.Errorf("no origin/referer: %d, want 403", w.Code)
	}
	// Same origin passes the gate; an undecodable credential id then yields 400.
	if w := call("not-b64!", map[string]string{"Origin": "https://" + s.cfg.Domain}); w.Code != http.StatusBadRequest {
		t.Errorf("same origin with bad id: %d, want 400", w.Code)
	}
	// Anonymous stays 401 (session check precedes the origin gate).
	r := httptest.NewRequest(http.MethodPost, "/auth/passkey/remove?id=AA", nil)
	w := httptest.NewRecorder()
	s.handlePasskeyRemove(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d, want 401", w.Code)
	}
	// GET must not mutate.
	g := httptest.NewRequest(http.MethodGet, "/auth/passkey/remove?id=AA", nil)
	g.AddCookie(s.sessionCookie("p@x.com"))
	g.Header.Set("Origin", "https://"+s.cfg.Domain)
	gw := httptest.NewRecorder()
	s.handlePasskeyRemove(gw, g)
	if gw.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d, want 405", gw.Code)
	}
}
