package server

// #17 (final slice): table-driven /api/register branch fence. Tier-whitelist,
// email normalization and duplicate conflicts already had dedicated tests
// (#1 follow-ups); this table closes the remaining branches: method guard,
// rate limit, malformed JSON, invalid email.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegisterTableDriven(t *testing.T) {
	cases := []struct {
		name   string
		method string
		body   string
		pre    func(t *testing.T, s *Server, req *http.Request) // per-case setup
		want   int
	}{
		{name: "method guard", method: http.MethodGet, body: `{}`, want: http.StatusMethodNotAllowed},
		{name: "bad json", method: http.MethodPost, body: `{nope`, want: http.StatusBadRequest},
		{name: "invalid email", method: http.MethodPost, body: `{"email":"not-an-email"}`, want: http.StatusBadRequest},
		{name: "oversize email", method: http.MethodPost, body: `{"email":"` + strings.Repeat("a", 250) + `@x.com"}`, want: http.StatusBadRequest},
		{name: "rate limited", method: http.MethodPost, body: `{"email":"ok@x.com"}`,
			pre: func(t *testing.T, s *Server, req *http.Request) {
				// exhaust the per-IP budget from the same address first
				for i := 0; i < 50; i++ {
					r := httptest.NewRequest(http.MethodPost, "/api/register", strings.NewReader(`{"email":"flood`+string(rune('a'+i))+`@x.com"}`))
					r.RemoteAddr = "9.9.9.9:1"
					rec := httptest.NewRecorder()
					s.handleRegister(rec, r)
					if rec.Code == http.StatusTooManyRequests {
						return // limiter already tripped
					}
				}
				t.Fatal("limiter never tripped after 50 registrations")
			},
			want: http.StatusTooManyRequests},
		{name: "paid tier rejected", method: http.MethodPost, body: `{"email":"p@x.com","tier":"pro"}`, want: http.StatusPaymentRequired},
		{name: "happy free", method: http.MethodPost, body: `{"email":"happy@x.com"}`, want: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newBillingTestServer(t)
			req := httptest.NewRequest(tc.method, "/api/register", strings.NewReader(tc.body))
			req.RemoteAddr = "9.9.9.9:1"
			if tc.pre != nil {
				tc.pre(t, s, req)
			}
			rec := httptest.NewRecorder()
			s.handleRegister(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("want %d, got %d %s", tc.want, rec.Code, rec.Body.String())
			}
		})
	}
}
