package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func newAdminTestServer(t *testing.T, adminToken string) *Server {
	t.Helper()
	cfg := Config{
		Domain:       "t.test",
		Addr:         "127.0.0.1:1",
		TLS:          TLSConfig{Mode: TLSModeHTTP},
		AccountsFile: filepath.Join(t.TempDir(), "a.json"),
		Tiers:        DefaultTiers(),
		AdminToken:   adminToken,
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func doRegister(s *Server, ip, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/register", strings.NewReader(body))
	req.RemoteAddr = ip + ":1111"
	rec := httptest.NewRecorder()
	s.handleRegister(rec, req)
	return rec
}

// #1 (follow-up to TestRegisterForcesFreeTier): register normalizes the email
// exactly like the OAuth paths, so case/space variants are the same mailbox
// and cannot multiply quotas.
func TestRegisterEmailNormalizedAndUnique(t *testing.T) {
	s := newAdminTestServer(t, "")
	rec := doRegister(s, "10.0.1.1", `{"email":"  Bob@X.com "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := s.accounts.LookupEmail("bob@x.com"); !ok {
		t.Fatal("email not normalized to bob@x.com")
	}
	// same mailbox in different case => 409, not a second account
	rec = doRegister(s, "10.0.1.2", `{"email":"BOB@x.com"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate (case-variant) email: want 409, got %d", rec.Code)
	}
	if got := len(s.accounts.List()); got != 1 {
		t.Fatalf("want 1 account after duplicate register, got %d", got)
	}
}

// #7 (complement to TestAdminFailsClosedWithoutToken): /stats enforces the
// token when one is configured but stays open for local ops when it is not.
func TestStatsAuthOptional(t *testing.T) {
	// no token: stats stays reachable, mutating admin routes stay closed
	s := newAdminTestServer(t, "")
	rec := httptest.NewRecorder()
	s.adminHandler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("stats without token configured: want 200, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.adminHandler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/accounts", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("accounts without token configured: want 503, got %d", rec.Code)
	}

	// token configured: stats now requires it too
	s2 := newAdminTestServer(t, "sekrit")
	rec = httptest.NewRecorder()
	s2.adminHandler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stats", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("stats without header: want 401, got %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/stats", nil)
	req.Header.Set("X-Admin-Token", "sekrit")
	rec = httptest.NewRecorder()
	s2.adminHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("stats with token: want 200, got %d", rec.Code)
	}
}

// #5: only fully-settled PayPal orders may activate.
func TestPayPalCaptureCompleted(t *testing.T) {
	cases := []struct {
		name  string
		order map[string]any
		want  bool
	}{
		{"settled", map[string]any{"status": "COMPLETED", "purchase_units": []any{map[string]any{
			"payments": map[string]any{"captures": []any{map[string]any{"status": "COMPLETED"}}}}}}, true},
		{"echeck pending capture", map[string]any{"status": "COMPLETED", "purchase_units": []any{map[string]any{
			"payments": map[string]any{"captures": []any{map[string]any{"status": "PENDING"}}}}}}, false},
		{"order pending", map[string]any{"status": "PENDING"}, false},
		{"no capture info", map[string]any{"status": "COMPLETED"}, true},
	}
	for _, c := range cases {
		if got := paypalCaptureCompleted(c.order); got != c.want {
			t.Errorf("%s: want %v got %v", c.name, c.want, got)
		}
	}
}

// #3: clearly-underpaid finished payments must not activate.
func TestNowPaymentsPaidEnough(t *testing.T) {
	if !nowpaymentsPaidEnough(map[string]any{"price_amount": 9.0, "outcome_amount": 9.0}) {
		t.Error("fully paid rejected")
	}
	if nowpaymentsPaidEnough(map[string]any{"price_amount": 9.0, "outcome_amount": 0.1}) {
		t.Error("underpayment accepted")
	}
	if !nowpaymentsPaidEnough(map[string]any{}) {
		t.Error("absent fields must pass (status gate still applies)")
	}
}
