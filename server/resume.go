package server

// Resume tokens let a reconnecting client reclaim the subdomain it had before
// the disconnect, including across a server restart. They are self-validating:
// HMAC(secret, sub|exp) with the expiry inside the signed payload, so no
// server-side session state is needed (a restart loses memory, not the key).

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// resumeTTL is how long after a Welcome the client may reclaim the subdomain.
// Long enough to cover a restart and a few backoff rounds, short enough that a
// leaked token is not a permanent squat.
const resumeTTL = 30 * time.Minute

var (
	resumeKeyOnce sync.Once
	resumeKey     []byte
)

// resumeSecret returns the HMAC key: SESSION_SECRET when set (so tokens
// survive restarts), otherwise a process-local random key (tokens die with
// the process, which is the safe default).
func resumeSecret() []byte {
	resumeKeyOnce.Do(func() {
		if v := os.Getenv("SESSION_SECRET"); v != "" {
			resumeKey = []byte("resume|" + v)
			return
		}
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		resumeKey = b
	})
	return resumeKey
}

var errResumeInvalid = errors.New("etunnel: invalid or expired resume token")

// issueResumeToken signs sub with an expiry.
func issueResumeToken(sub string, now time.Time) string {
	exp := now.Add(resumeTTL).Unix()
	payload := fmt.Sprintf("%s|%d", sub, exp)
	mac := hmac.New(sha256.New, resumeSecret())
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sig
}

// verifyResumeToken returns the subdomain carried by a valid token.
func verifyResumeToken(tok string, now time.Time) (string, error) {
	body, sig, ok := strings.Cut(tok, ".")
	if !ok || body == "" || sig == "" {
		return "", errResumeInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return "", errResumeInvalid
	}
	mac := hmac.New(sha256.New, resumeSecret())
	mac.Write(raw)
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return "", errResumeInvalid
	}
	sub, expStr, ok := strings.Cut(string(raw), "|")
	if !ok || sub == "" {
		return "", errResumeInvalid
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || now.Unix() > exp {
		return "", errResumeInvalid
	}
	return sub, nil
}
