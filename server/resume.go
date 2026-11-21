package server

// Resume tokens let a reconnecting client reclaim the subdomain it had before
// the disconnect, including across a server restart. They are self-validating:
// HMAC(secret, payload) with the expiry inside the signed payload, so no
// server-side session state is needed (a restart loses memory, not the key).
//
// #292: the signed payload binds the token to an identity
// (sub|exp|owner|sexp|custom). `owner` ties the token to the account that
// held the tunnel (or, for anonymous tunnels, the client IP), so a leaked
// token cannot be replayed by a different identity. `sexp` carries the
// session's hard expiry, so a chain of resume handshakes can never extend a
// session past the TTL it was originally granted (rolling-resume renewal
// cap). Tokens issued before #292 (payload `sub|exp`) fail verification and
// the handshake falls back to a fresh subdomain - a graceful migration with
// no protocol change (the token is opaque to clients).

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

// resumeOwner returns the identity a resume token is bound to (#292): the
// account behind the tunnel, or the client IP for anonymous tunnels. A
// verified token is only honored when this binding matches the presenting
// connection, so a leaked token is not an offline takeover.
func resumeOwner(acct *Account, ip string) string {
	if acct != nil {
		return "a:" + acct.Email
	}
	return "i:" + ip
}

// issueResumeToken signs sub for owner with two expiries: `exp` bounds how
// long the token itself may be replayed, `sessExpiry` (the session's hard
// TTL, zero when the tier is unbounded) is carried inside the signature so a
// resumed session keeps its original lifetime and a chain of resume
// handshakes can never outlive it (#292). `custom` records whether the
// subdomain was a requested (ownership-tracked) name, so resume can apply
// the same plan gates the request path applies.
func issueResumeToken(sub, owner string, sessExpiry time.Time, custom bool, now time.Time) string {
	exp := now.Add(resumeTTL).Unix()
	sexp := int64(0)
	if !sessExpiry.IsZero() {
		sexp = sessExpiry.Unix()
		if sexp < exp {
			exp = sexp // a token never outlives the session it belongs to
		}
	}
	c := int64(0)
	if custom {
		c = 1
	}
	payload := fmt.Sprintf("%s|%d|%s|%d|%d", sub, exp, owner, sexp, c)
	mac := hmac.New(sha256.New, resumeSecret())
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sig
}

// resumedToken is the verified content of a resume token.
type resumedToken struct {
	sub        string
	sessExpiry time.Time // zero: the tier had no TTL
	custom     bool      // requested (ownership-tracked) subdomain
}

// verifyResumeToken validates tok for the presenting identity wantOwner and
// returns the subdomain it reclaims. Tokens bound to a different identity,
// expired tokens, and pre-#292 tokens (different payload arity) are all
// rejected - the caller falls back to normal subdomain resolution.
func verifyResumeToken(tok, wantOwner string, now time.Time) (resumedToken, error) {
	body, sig, ok := strings.Cut(tok, ".")
	if !ok || body == "" || sig == "" {
		return resumedToken{}, errResumeInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return resumedToken{}, errResumeInvalid
	}
	mac := hmac.New(sha256.New, resumeSecret())
	mac.Write(raw)
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return resumedToken{}, errResumeInvalid
	}
	fields := strings.Split(string(raw), "|")
	if len(fields) != 5 || fields[0] == "" || fields[2] == "" {
		return resumedToken{}, errResumeInvalid
	}
	exp, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || now.Unix() > exp {
		return resumedToken{}, errResumeInvalid
	}
	if fields[2] != wantOwner {
		return resumedToken{}, errResumeInvalid
	}
	rt := resumedToken{sub: fields[0]}
	if s, err := strconv.ParseInt(fields[3], 10, 64); err == nil && s > 0 {
		rt.sessExpiry = time.Unix(s, 0)
	}
	rt.custom = fields[4] == "1"
	if !rt.sessExpiry.IsZero() && now.After(rt.sessExpiry) {
		return resumedToken{}, errResumeInvalid
	}
	return rt, nil
}
