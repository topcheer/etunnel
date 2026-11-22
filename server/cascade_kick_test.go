package server

// #350/#351 regressions: account deletion and entitlement revocation
// cascade to the holder's live sessions; #356: List returns value copies.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func stripeSignedBody(t *testing.T, secret, body string) *http.Request {
	t.Helper()
	ts := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write([]byte(body))
	sig := hex.EncodeToString(mac.Sum(nil))
	r := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(body))
	r.Header.Set("Stripe-Signature", fmt.Sprintf("t=%d,v1=%s", ts, sig))
	return r
}

func seededSession(t *testing.T, s *Server, email, sub string) (*Session, string) {
	t.Helper()
	acct, err := s.activate(email, "pro")
	if err != nil {
		t.Fatal(err)
	}
	snap, ok := s.accounts.Snapshot(acct)
	if !ok {
		t.Fatal("activated account vanished")
	}
	sess := &Session{
		srv:     s,
		id:      "sess-" + sub,
		sub:     sub,
		url:     "https://" + sub + ".t.test",
		ip:      "203.0.113.9:4444",
		account: &snap,
		closed:  make(chan struct{}),
	}
	if err := s.reg.Acquire(sub, sess); err != nil {
		t.Fatalf("seed %s: %v", sub, err)
	}
	return sess, snap.APIKey
}

// #350 HIGH: telegram /delete cascades - the holder's session closes with a
// readable reason and the registry slot is freed.
func TestDeleteAccountKicksSessions(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	sess, _ := seededSession(t, s, "del@x.com", "del-t1")

	if got := s.handleAdminCommand("/delete del@x.com"); !strings.Contains(got, "deleted") {
		t.Fatalf("delete reply: %q", got)
	}

	select {
	case <-sess.closed:
	default:
		t.Fatal("account deletion did not close the holder's session (#350)")
	}
	if reason := sess.CloseReason(); !strings.Contains(reason, "deleted") {
		t.Fatalf("close reason = %q, want the deletion reason (#350)", reason)
	}
	if s.reg.Lookup("del-t1") != nil {
		t.Fatal("registry still serves the deleted account's tunnel (#350)")
	}
}

// #350 HIGH: the admin route cascades too.
func TestAdminDeleteKicksSessions(t *testing.T) {
	s := newBillingTestServer(t)
	s.cfg.AdminToken = "tok"
	sess, key := seededSession(t, s, "adm@x.com", "adm-t1")

	// Drive the REAL admin delete handler (route path: /admin/accounts/<key>).
	r := httptest.NewRequest(http.MethodDelete, "/admin/accounts/"+key, nil)
	r.Header.Set("X-Admin-Token", "tok")
	w := httptest.NewRecorder()
	s.handleAdminAccountDelete(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("admin delete: %d %s", w.Code, w.Body.String())
	}

	select {
	case <-sess.closed:
	default:
		t.Fatal("admin deletion did not close the holder's session (#350)")
	}
}

// #351: cancel and the reaper downgrade both close live sessions.
func TestDowngradePathsKickSessions(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		s := newBillingTestServer(t)
		sess, _ := seededSession(t, s, "cx@x.com", "cx-t1")
		if !s.accounts.CancelSubscription("cx@x.com") {
			t.Fatal("cancel failed")
		}
		if a, ok := s.accounts.LookupEmail("cx@x.com"); ok {
			s.kickSessionsByAccount(a.APIKey, "subscription cancelled")
		}
		select {
		case <-sess.closed:
		default:
			t.Fatal("cancel did not close the live session (#351)")
		}
	})

	t.Run("reaper", func(t *testing.T) {
		s := newBillingTestServer(t)
		sess, _ := seededSession(t, s, "rp@x.com", "rp-t1")
		s.accounts.Mutate("rp@x.com", func(a *Account) { a.ExpiresAt = time.Now().Add(-time.Hour) })
		if s.reapExpiredTiers(time.Now()) == 0 {
			t.Fatal("reaper reaped nothing")
		}
		select {
		case <-sess.closed:
		default:
			t.Fatal("reaper downgrade did not close the live session (#351)")
		}
	})
}

// #351: the Stripe refund/chargeback rollback kicks live sessions.
func TestStripeRollbackKicksSessions(t *testing.T) {
	s := newBillingTestServer(t)
	s.billing.StripeWebhookSecret = "whsec"
	sess, _ := seededSession(t, s, "rb@x.com", "rb-t1")

	body := `{"id":"evt_rb1","type":"charge.refunded","data":{"object":{"metadata":{"email":"rb@x.com"}}}}`
	w := httptest.NewRecorder()
	s.handleStripeWebhook(w, stripeSignedBody(t, "whsec", body))
	if w.Code != http.StatusOK {
		t.Fatalf("rollback webhook: %d %s", w.Code, w.Body.String())
	}
	select {
	case <-sess.closed:
	default:
		t.Fatal("refund rollback did not close the live session (#351)")
	}
}

// #351: ResetKey tears down tunnels authenticated by the old key.
func TestResetKeyKicksSessions(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	sess, _ := seededSession(t, s, "rk@x.com", "rk-t1")
	a, _ := s.accounts.LookupEmail("rk@x.com")

	// The /me reset path: epoch bump + kick cascade by the CURRENT key.
	s.accounts.Mutate("rk@x.com", func(x *Account) { x.SessionEpoch++ })
	s.kickSessionsByAccount(a.APIKey, "API key reset")

	select {
	case <-sess.closed:
	default:
		t.Fatal("reset did not tear down the old-key tunnel (#351)")
	}
}

// #356: List hands out value copies - mutating an element must not leak
// into the store's live account.
func TestListReturnsValueCopies(t *testing.T) {
	s := newBillingTestServer(t)
	if _, err := s.activate("cp@x.com", "pro"); err != nil {
		t.Fatal(err)
	}
	for _, a := range s.accounts.List() {
		if a.Email == "cp@x.com" {
			a.Tier = "hacked"
			break
		}
	}
	if a, _ := s.accounts.LookupEmail("cp@x.com"); a.Tier == "hacked" {
		t.Fatal("List returned a live pointer - mutation leaked into the store (#356)")
	}
}
