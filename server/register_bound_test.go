package server

// #193: the public /api/register endpoint must bound its request body like
// every other unauthenticated endpoint - an unbounded read is a cheap
// memory-exhaustion vector.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegisterRejectsOversizedBody(t *testing.T) {
	s := newBillingTestServer(t)

	big := `{"email":"` + strings.Repeat("a", 64*1024) + `@x.com","tier":"free"}`
	req := httptest.NewRequest(http.MethodPost, "/api/register", strings.NewReader(big))
	rec := httptest.NewRecorder()
	s.handleRegister(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized body: want 400, got %d", rec.Code)
	}

	ok := `{"email":"reg-test@x.com","tier":"free"}`
	req2 := httptest.NewRequest(http.MethodPost, "/api/register", strings.NewReader(ok))
	rec2 := httptest.NewRecorder()
	s.handleRegister(rec2, req2)
	if rec2.Code == http.StatusBadRequest {
		t.Fatalf("normal-sized body rejected: %d %s", rec2.Code, rec2.Body.String())
	}
}
