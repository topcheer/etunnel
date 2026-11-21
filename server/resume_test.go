package server

// #292: resume tokens are bound to the identity that received them
// (account, or client IP for anonymous tunnels) and carry the session's
// hard expiry, so a leaked token is not an offline takeover and a chain of
// resume handshakes cannot extend a session past its original TTL.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"testing"
	"time"
)

func TestResumeTokenRoundTrip(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	sexp := now.Add(4 * time.Hour)
	tok := issueResumeToken("mydev", "a:dev@example.com", sexp, true, now)

	rt, err := verifyResumeToken(tok, "a:dev@example.com", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("verify same identity: %v", err)
	}
	if rt.sub != "mydev" || !rt.custom || !rt.sessExpiry.Equal(sexp) {
		t.Fatalf("round trip mismatch: %+v", rt)
	}
}

func TestResumeTokenRejectsForeignIdentity(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tok := issueResumeToken("mydev", "a:dev@example.com", time.Time{}, true, now)
	if _, err := verifyResumeToken(tok, "a:other@example.com", now); err == nil {
		t.Fatal("cross-account replay must be rejected (#292 F1)")
	}
	if _, err := verifyResumeToken(tok, "i:203.0.113.9", now); err == nil {
		t.Fatal("anonymous replay of an account-bound token must be rejected")
	}
}

func TestResumeTokenBoundToIP(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tok := issueResumeToken("quick-fox-42ab", "i:203.0.113.9", time.Time{}, false, now)
	if _, err := verifyResumeToken(tok, "i:203.0.113.9", now.Add(time.Minute)); err != nil {
		t.Fatalf("same-IP anonymous resume must pass: %v", err)
	}
	if _, err := verifyResumeToken(tok, "i:198.51.100.7", now); err == nil {
		t.Fatal("anonymous replay from another IP must be rejected (#292 F1)")
	}
}

func TestResumeTokenNeverOutlivesSession(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	sexp := now.Add(5 * time.Minute) // shorter than the 30min resume window
	tok := issueResumeToken("x", "i:9.9.9.9", sexp, false, now)

	// Just inside the session expiry the token verifies and carries the
	// original hard expiry (resume keeps, never extends, the TTL).
	rt, err := verifyResumeToken(tok, "i:9.9.9.9", sexp.Add(-time.Second))
	if err != nil {
		t.Fatalf("verify inside session expiry: %v", err)
	}
	if !rt.sessExpiry.Equal(sexp) {
		t.Fatalf("session expiry not carried: %v want %v", rt.sessExpiry, sexp)
	}
	// After the session's original expiry the token is dead, even though
	// far less than resumeTTL has passed - rolling resumes cannot extend.
	if _, err := verifyResumeToken(tok, "i:9.9.9.9", sexp.Add(time.Second)); err == nil {
		t.Fatal("token must die with the session it belongs to (#292 F3)")
	}
}

func TestResumeTokenRejectsLegacyFormat(t *testing.T) {
	// Pre-#292 payload (sub|exp), correctly signed under the same key:
	// must be rejected, not silently honored (payload arity check).
	now := time.Unix(1_800_000_000, 0)
	payload := "mydev|" + strconv.FormatInt(now.Add(resumeTTL).Unix(), 10)
	mac := hmac.New(sha256.New, resumeSecret())
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	tok := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sig
	if _, err := verifyResumeToken(tok, "a:dev@example.com", now); err == nil {
		t.Fatal("legacy sub|exp token must be rejected (#292 migration)")
	}
}
