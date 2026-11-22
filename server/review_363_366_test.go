package server

// #363/#366 regressions: /me reset kicks via the OLD key through the real
// handler (handler-level, not a direct assistant call); handshake-vs-delete
// window re-check; Delete surfaces persistence errors; kick budget.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// #363 HIGH: drive the REAL /me reset handler. The account's live key
// rotates during ResetKey; sessions hold handshake copies of the OLD key
// and must be kicked against it (the #351 regression used the unchanged
// key and proved nothing).
func TestMeResetKicksOldKeySessions(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	acct, err := s.activate("r363@x.com", "pro")
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := s.accounts.Snapshot(acct)
	oldKey := snap.APIKey

	// Session holds a handshake copy of the account snapshot (old key).
	sess := &Session{
		srv:     s,
		id:      "s363",
		sub:     "r363-t1",
		url:     "https://r363-t1.t.test",
		account: &snap,
		closed:  make(chan struct{}),
	}
	if err := s.reg.Acquire("r363-t1", sess); err != nil {
		t.Fatal(err)
	}

	// Real /me reset via handleMeAction (session-authenticated).
	req := httptest.NewRequest(http.MethodPost, "/me/reset",
		strings.NewReader(`{"email":"r363@x.com"}`))
	req.Header.Set("Origin", "https://"+s.cfg.Domain) // #293 same-origin gate
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(s.sessionCookie("r363@x.com"))
	w := httptest.NewRecorder()
	s.handleMeAction(w, req, "reset")
	if w.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), oldKey) {
		t.Fatal("reset response leaked the OLD key")
	}

	// The OLD-key session must be closed (pre-#363 code kicked against the
	// NEW key -> no match -> session stayed open until TTL).
	select {
	case <-sess.closed:
	default:
		t.Fatal("old-key session survived /me reset (#363)")
	}
	// A fresh snapshot carries the NEW key; new tunnels authenticate fine.
	if snap2, ok := s.accounts.Snapshot(acct); ok && snap2.APIKey == oldKey {
		t.Fatal("ResetKey did not rotate the key")
	}
}

// #366 LOW: the kick budget caps synchronous ledger work - 20 sessions are
// kicked well under the budget and the count reflects reality.
func TestKickSessionsByAccountBudget(t *testing.T) {
	s := newBillingTestServer(t)
	acct, err := s.activate("b@x.com", "pro")
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := s.accounts.Snapshot(acct)
	for i := 0; i < 20; i++ {
		sess := &Session{
			srv:     s,
			id:      fmt.Sprintf("k%d", i),
			sub:     fmt.Sprintf("k-sub-%d", i),
			account: &snap,
			closed:  make(chan struct{}),
		}
		if err := s.reg.Acquire(sess.sub, sess); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	n := s.kickSessionsByAccount(snap.APIKey, "test kick")
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("kick budget blown: %d sessions took %v (#366 LOW)", n, elapsed)
	}
	if n != 20 {
		t.Fatalf("kick counted %d, want 20", n)
	}
}
