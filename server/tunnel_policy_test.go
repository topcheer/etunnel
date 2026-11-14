package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func mustPolicy(t *testing.T, raw string) *TunnelPolicy {
	t.Helper()
	tp, err := parseTunnelPolicy(parseRaw(t, raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return tp
}

func parseRaw(t *testing.T, s string) *rawPolicy {
	t.Helper()
	var r rawPolicy
	if err := jsonUnmarshalStr(s, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &r
}

func jsonUnmarshalStr(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}

func TestPolicyParseErrors(t *testing.T) {
	bad := []string{
		`{}`,                         // empty
		`{"basicAuth":"nopass"}`,     // no colon
		`{"basicAuth":":p"}`,         // no user
		`{"allowIPs":["not-an-ip"]}`, // bad CIDR
		`{"webhooks":[{"provider":"unknown","secret":"x"}]}`,
		`{"webhooks":[{"provider":"stripe"}]}`, // missing secret
	}
	for _, in := range bad {
		if _, err := parseTunnelPolicy(parseRaw(t, in)); err == nil {
			t.Errorf("policy %s should fail", in)
		}
	}
	// nil policy -> nil (legacy path)
	if tp, err := parseTunnelPolicy(nil); tp != nil || err != nil {
		t.Fatalf("nil policy: %v %v", tp, err)
	}
}

func TestPolicyBasicAuth(t *testing.T) {
	tp := mustPolicy(t, `{"basicAuth":"alice:s3cret"}`)
	r := httptest.NewRequest("POST", "/hook", nil)
	r.SetBasicAuth("alice", "s3cret")
	w := httptest.NewRecorder()
	if !tp.apply(w, r, nil) {
		t.Fatalf("correct creds rejected: %d", w.Code)
	}
	r2 := httptest.NewRequest("POST", "/hook", nil)
	r2.SetBasicAuth("alice", "wrong")
	w2 := httptest.NewRecorder()
	if tp.apply(w2, r2, nil) || w2.Code != http.StatusUnauthorized || w2.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("wrong creds: code=%d www-auth=%q", w2.Code, w2.Header().Get("WWW-Authenticate"))
	}
}

func TestPolicyAllowIPs(t *testing.T) {
	tp := mustPolicy(t, `{"allowIPs":["10.0.0.0/8","192.168.1.7"]}`)
	// inside CIDR
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.1.2.3:9999"
	if !tp.apply(httptest.NewRecorder(), r, nil) {
		t.Fatal("in-CIDR rejected")
	}
	// exact IP (single, auto-/32)
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.RemoteAddr = "192.168.1.7:1234"
	if !tp.apply(httptest.NewRecorder(), r2, nil) {
		t.Fatal("exact IP rejected")
	}
	// outside
	r3 := httptest.NewRequest("GET", "/", nil)
	r3.RemoteAddr = "8.8.8.8:1"
	w := httptest.NewRecorder()
	if tp.apply(w, r3, nil) || w.Code != http.StatusForbidden {
		t.Fatalf("outside IP: %d", w.Code)
	}
	// Forwarded headers honoured ONLY when the operator opted in (#140):
	// spoof test first (default off: header ignored, socket truth wins) ...
	sp := httptest.NewRequest("GET", "/", nil)
	sp.RemoteAddr = "104.28.1.1:1"                // attacker socket, outside list
	sp.Header.Set("CF-Connecting-IP", "10.9.9.9") // spoofed, inside list
	wsp := httptest.NewRecorder()
	if tp.apply(wsp, sp, nil) || wsp.Code != http.StatusForbidden {
		t.Fatalf("spoofed CF-Connecting-IP must not bypass allowIPs by default: %d", wsp.Code)
	}
	// ... then with trust enabled (CF-fronted deployment).
	trustForwardedFor = true
	defer func() { trustForwardedFor = false }()
	r4 := httptest.NewRequest("GET", "/", nil)
	r4.RemoteAddr = "104.28.1.1:1" // CF edge, outside list
	r4.Header.Set("CF-Connecting-IP", "10.9.9.9")
	if !tp.apply(httptest.NewRecorder(), r4, nil) {
		t.Fatal("CF-Connecting-IP not honoured when TRUST_FORWARDED_FOR is on")
	}
}

func TestPolicyWebhooksStripe(t *testing.T) {
	secret := "whsec_test"
	body := []byte(`{"id":"evt_1"}`)
	ts := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(fmt.Sprintf("%d.", ts)))
	mac.Write(body)
	sig := fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))

	tp := mustPolicy(t, `{"webhooks":[{"provider":"stripe","secret":"whsec_test"}]}`)

	// valid signature -> pass + marker header
	r := httptest.NewRequest("POST", "/hook", nil)
	r.Header.Set("Stripe-Signature", sig)
	if !tp.apply(httptest.NewRecorder(), r, body) {
		t.Fatal("valid stripe signature rejected")
	}
	if got := r.Header.Get("X-Etunnel-Webhook"); got != "verified: stripe" {
		t.Fatalf("marker header = %q", got)
	}
	// tampered body -> 401
	r2 := httptest.NewRequest("POST", "/hook", nil)
	r2.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	if tp.apply(w, r2, []byte(`{"id":"tampered"}`)) || w.Code != http.StatusUnauthorized {
		t.Fatalf("tampered stripe body: %d", w.Code)
	}
	// stale timestamp -> 401
	stale := fmt.Sprintf("t=%d,v1=%s", ts-3600, hex.EncodeToString(mac.Sum(nil)))
	r3 := httptest.NewRequest("POST", "/hook", nil)
	r3.Header.Set("Stripe-Signature", stale)
	w3 := httptest.NewRecorder()
	if tp.apply(w3, r3, body) || w3.Code != http.StatusUnauthorized {
		t.Fatalf("stale stripe ts: %d", w3.Code)
	}
	// no signature header -> fall through (allowed)
	r4 := httptest.NewRequest("POST", "/hook", nil)
	if !tp.apply(httptest.NewRecorder(), r4, body) {
		t.Fatal("unsigned request should fall through, not be rejected")
	}
}

func TestPolicyWebhooksGitHub(t *testing.T) {
	secret := "gh_secret"
	body := []byte(`{"action":"opened"}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	tp := mustPolicy(t, `{"webhooks":[{"provider":"github","secret":"gh_secret"}]}`)

	r := httptest.NewRequest("POST", "/hook", nil)
	r.Header.Set("X-Hub-Signature-256", sig)
	if !tp.apply(httptest.NewRecorder(), r, body) {
		t.Fatal("valid github signature rejected")
	}
	r2 := httptest.NewRequest("POST", "/hook", nil)
	r2.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	w := httptest.NewRecorder()
	if tp.apply(w, r2, body) || w.Code != http.StatusUnauthorized {
		t.Fatalf("bad github signature: %d", w.Code)
	}
}

func TestPolicyWebhooksGitLab(t *testing.T) {
	tp := mustPolicy(t, `{"webhooks":[{"provider":"gitlab","secret":"gl_tok"}]}`)
	r := httptest.NewRequest("POST", "/hook", nil)
	r.Header.Set("X-Gitlab-Token", "gl_tok")
	if !tp.apply(httptest.NewRecorder(), r, nil) {
		t.Fatal("valid gitlab token rejected")
	}
	r2 := httptest.NewRequest("POST", "/hook", nil)
	r2.Header.Set("X-Gitlab-Token", "nope")
	w := httptest.NewRecorder()
	if tp.apply(w, r2, nil) || w.Code != http.StatusUnauthorized {
		t.Fatalf("bad gitlab token: %d", w.Code)
	}
}

func TestPolicyNeedsBody(t *testing.T) {
	if (&TunnelPolicy{}).needsBody() {
		t.Fatal("empty policy must not need body")
	}
	if !mustPolicy(t, `{"webhooks":[{"provider":"gitlab","secret":"x"}]}`).needsBody() {
		t.Fatal("webhook policy must need body")
	}
	if mustPolicy(t, `{"basicAuth":"a:b"}`).needsBody() {
		t.Fatal("basicAuth must stay streaming")
	}
}
