package server

// OAuth login (Google / GitLab / Apple) + signed session cookies.
//
// Env (per provider; empty = provider hidden from the login page):
//   GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET
//   GITLAB_CLIENT_ID / GITLAB_CLIENT_SECRET / GITLAB_URL (default gitlab.com)
//   APPLE_CLIENT_ID / APPLE_TEAM_ID / APPLE_KEY_ID / APPLE_PRIVATE_KEY (p8 PEM)
//   SESSION_SECRET (HMAC key for cookies; random per boot when unset)
//
// Flow: /auth/<provider>/login -> provider consent -> /auth/<provider>/callback
// -> find-or-create account (provider+subject identity) -> session cookie -> /me
// Billing requires a session: the checkout endpoint pins the logged-in email.

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type oauthConfig struct {
	sessionSecret string

	googleID, googleSecret string
	gitlabID, gitlabSecret string
	gitlabURL              string
	githubID, githubSecret string
	appleID, appleTeam,
	appleKey string
	appleKeyPEM string
}

func oauthFromEnv() oauthConfig {
	return oauthConfig{
		sessionSecret: os.Getenv("SESSION_SECRET"),
		googleID:      os.Getenv("GOOGLE_CLIENT_ID"),
		googleSecret:  os.Getenv("GOOGLE_CLIENT_SECRET"),
		gitlabID:      os.Getenv("GITLAB_CLIENT_ID"),
		gitlabSecret:  os.Getenv("GITLAB_CLIENT_SECRET"),
		gitlabURL:     envOr("GITLAB_URL", "https://gitlab.com"),
		githubID:      os.Getenv("GITHUB_CLIENT_ID"),
		githubSecret:  os.Getenv("GITHUB_CLIENT_SECRET"),
		appleID:       os.Getenv("APPLE_CLIENT_ID"),
		appleTeam:     os.Getenv("APPLE_TEAM_ID"),
		appleKey:      os.Getenv("APPLE_KEY_ID"),
		appleKeyPEM:   loadPEMValue(os.Getenv("APPLE_PRIVATE_KEY")),
	}
}

// bootSecret is the process-local fallback key used when SESSION_SECRET is
// unset. sync.Once both builds it exactly once and publishes it: without it, two
// goroutines racing on the first request each generated a key and the second
// write silently invalidated cookies signed with the first, so sessions failed at
// random right after startup (#221). resumeSecret uses the same pattern.
//
// A plain []byte is enough - Once.Do establishes the happens-before edge, so no
// atomic.Value is needed and there is no "unreachable" branch to get wrong.
var (
	bootSecretOnce sync.Once
	bootSecret     []byte
)

func (s *Server) sessionKey() []byte {
	if s.auth.sessionSecret != "" {
		return []byte(s.auth.sessionSecret)
	}
	bootSecretOnce.Do(func() {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		bootSecret = b
	})
	return bootSecret
}

// sessionCookie mints "email|expiry|hex(hmac)". The MAC folds the
// account's session epoch (#295): bumping it (logout, API-key reset)
// invalidates every cookie minted before the bump.
func (s *Server) sessionCookie(email string) *http.Cookie {
	exp := time.Now().Add(30 * 24 * time.Hour).Unix()
	epoch := int64(0)
	if a, ok := s.accounts.LookupEmail(email); ok {
		epoch = a.SessionEpoch
	}
	mac := hmacStr(s.sessionKey(), fmt.Sprintf("%s|%d|%d", email, exp, epoch))
	v := fmt.Sprintf("%s|%d|%s", email, exp, mac)
	return &http.Cookie{Name: "et_session", Value: v, Path: "/", HttpOnly: true,
		Secure:   s.cfg.TLS.Mode != TLSModeHTTP, // #6: never ride plaintext HTTP when TLS is on
		SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 3600}
}

// sessionEmail validates the cookie and returns the account email (""=none).
func (s *Server) sessionEmail(r *http.Request) string {
	c, err := r.Cookie("et_session")
	if err != nil || c.Value == "" {
		return ""
	}
	email, exp, mac, ok := split3(c.Value)
	if !ok || time.Now().Unix() > exp {
		return ""
	}
	acct, ok := s.accounts.LookupEmail(email)
	if !ok {
		return "" // closed/renamed account invalidates the session
	}
	// #295: the epoch folds into the MAC - cookies minted before a logout
	// or API-key reset (epoch bump) fail this check while the account lives
	// on. Accounts created before the field existed carry epoch 0, so the
	// upgrade does not force a global re-login.
	if !hmacEqual(hmacStr(s.sessionKey(), fmt.Sprintf("%s|%d|%d", email, exp, acct.SessionEpoch)), mac) {
		return ""
	}
	return email
}

func split3(v string) (a string, b int64, c string, ok bool) {
	p := strings.Split(v, "|")
	if len(p) != 3 {
		return
	}
	var err error
	if b, err = parseInt64(p[1]); err != nil {
		return
	}
	return p[0], b, p[2], true
}

func (s *Server) oauthEnabled(p string) bool {
	switch p {
	case "google":
		return s.auth.googleID != ""
	case "gitlab":
		return s.auth.gitlabID != ""
	case "github":
		return s.auth.githubID != ""
	case "apple":
		return s.auth.appleID != "" && s.auth.appleTeam != "" && s.auth.appleKey != "" && s.auth.appleKeyPEM != ""
	}
	return false
}

// state cookie guards the code-flow redirect (CSRF). SameSite is
// provider-specific: redirect flows (google/gitlab/github) return via a
// top-level GET navigation, where Lax is sufficient and, critically,
// immune to third-party-cookie blocking (Chrome tracking protection drops
// SameSite=None cookies on the cross-site callback, which broke every
// redirect provider after 8778985 set None for Apple). Apple alone needs
// None because form_post submits a cross-site POST.
func (s *Server) setStateCookie(w http.ResponseWriter, provider string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	st := base64.RawURLEncoding.EncodeToString(b)
	sameSite := http.SameSiteLaxMode
	if provider == "apple" {
		sameSite = http.SameSiteNoneMode // Apple form_post is a cross-site POST
	}
	http.SetCookie(w, &http.Cookie{Name: "et_oauth_state", Value: st, Path: "/",
		HttpOnly: true, Secure: true, SameSite: sameSite, MaxAge: 600})
	return st
}

func (s *Server) checkStateCookie(w http.ResponseWriter, r *http.Request, state string) bool {
	if c, err := r.Cookie("et_oauth_state"); err != nil || c.Value == "" || c.Value != state || state == "" {
		var got string
		if c != nil {
			got = c.Value
		}
		s.logf("auth: state mismatch (cookie=%q query=%q)", got, state)
		return false
	}
	http.SetCookie(w, &http.Cookie{Name: "et_oauth_state", Value: "", Path: "/", MaxAge: -1})
	return true
}

// canonicalAuthHost reports whether the request is already on the service
// host; when it is not (apex marketing site, alias domains), it redirects to
// the same auth URL there and returns false so the caller stops.
func (s *Server) canonicalAuthHost(w http.ResponseWriter, r *http.Request) bool {
	if r.Host == s.cfg.Domain {
		return true
	}
	target := "https://" + s.cfg.Domain + r.URL.RequestURI()
	s.logf("auth: canonicalizing %s auth flow from host %q", r.URL.Path, r.Host)
	http.Redirect(w, r, target, http.StatusSeeOther)
	return false
}

var oauthHTTP = &http.Client{Timeout: 15 * time.Second}

func (s *Server) handleOAuthLogin(w http.ResponseWriter, r *http.Request, provider string) {
	if !s.oauthEnabled(provider) {
		http.Error(w, "provider not configured", http.StatusServiceUnavailable)
		return
	}
	// OAuth must start and land on the same host: the state cookie is
	// host-only, so a login started on the apex (etun.uk) but redirected back
	// to the service host (t.etun.uk) arrives cookieless and every provider
	// fails with "bad oauth state". Canonicalize first: one 303 hop to the
	// service domain with the full path and query preserved.
	if !s.canonicalAuthHost(w, r) {
		return
	}
	base := s.billing.PublicBaseURL
	st := s.setStateCookie(w, provider)
	var authURL string
	switch provider {
	case "google":
		authURL = "https://accounts.google.com/o/oauth2/v2/auth?" + url.Values{
			"client_id":     {s.auth.googleID},
			"redirect_uri":  {base + "/auth/google/callback"},
			"response_type": {"code"},
			"scope":         {"openid email profile"},
			"state":         {st},
		}.Encode()
	case "gitlab":
		authURL = s.auth.gitlabURL + "/oauth/authorize?" + url.Values{
			"client_id":     {s.auth.gitlabID},
			"redirect_uri":  {base + "/auth/gitlab/callback"},
			"response_type": {"code"},
			"scope":         {"read_user"},
			"state":         {st},
		}.Encode()
	case "github":
		authURL = "https://github.com/login/oauth/authorize?" + url.Values{
			"client_id":    {s.auth.githubID},
			"redirect_uri": {base + "/auth/github/callback"},
			"scope":        {"read:user user:email"},
			"state":        {st},
		}.Encode()
	case "apple":
		authURL = "https://appleid.apple.com/auth/authorize?" + url.Values{
			"client_id":     {s.auth.appleID},
			"redirect_uri":  {base + "/auth/apple/callback"},
			"response_type": {"code"},
			"response_mode": {"form_post"}, // required when scope includes name/email
			"scope":         {"name email"},
			"state":         {st},
		}.Encode()
	}
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

// validReturnTo reports whether v is a strictly same-site resume path:
// rooted and relative, no scheme/host/opaque part, no backslash (browsers
// normalise "\" to "/", so "/\\evil.com" becomes a protocol-relative hop
// to evil.com) and no control characters (#189).
func validReturnTo(v string) bool {
	if v == "" || !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") ||
		strings.ContainsAny(v, "\\\r\n\x00") {
		return false
	}
	u, err := url.Parse(v)
	if err != nil || u.IsAbs() || u.Host != "" || u.Opaque != "" || !strings.HasPrefix(u.Path, "/") {
		return false
	}
	return true
}

func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request, provider string) {
	if !s.oauthEnabled(provider) {
		http.Error(w, "provider not configured", http.StatusServiceUnavailable)
		return
	}
	if !s.canonicalAuthHost(w, r) {
		return
	}
	// Apple posts (response_mode=form_post); others redirect with query params.
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		if c := r.PostFormValue("code"); c != "" {
			code = c
		}
		if st := r.PostFormValue("state"); st != "" {
			state = st
		}
	}
	if code == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !s.checkStateCookie(w, r, state) {
		http.Error(w, "bad oauth state", http.StatusBadRequest)
		return
	}
	id, err := s.oauthIdentity(r.Context(), provider, code)
	if err != nil {
		s.logf("auth: %s callback failed: %v", provider, err)
		http.Error(w, "login failed, please retry", http.StatusBadGateway)
		return
	}
	// #215: an email the provider has not verified must never reach the
	// find-or-create logic below. The claim branch rotates the API key and
	// issues a session cookie, so trusting an unverified address is account
	// takeover for any address an attacker can type into a soft-confirm IdP.
	// Fail closed for the whole login.
	if !id.emailVerified {
		s.logf("auth: %s login rejected: email %q not verified by provider (subject %s)", provider, id.email, id.subject)
		http.Error(w, "email not verified by provider, verify it there first", http.StatusForbidden)
		return
	}
	// find-or-create by (provider, subject); link email as fallback identity.
	var acct *Account
	if a, ok := s.accounts.FindByProvider(provider, id.subject); ok {
		acct = a
	} else if _, ok := s.accounts.LookupEmail(id.email); ok {
		// Claim an existing anonymous (register-created) account. The API key
		// is rotated on claim (#2): whoever pre-registered this address loses
		// the key they were handed, so email squatting yields no persistence.
		s.accounts.Mutate(id.email, func(a *Account) {
			a.Provider, a.Subject, a.Name = provider, id.subject, id.name
		})
		s.accounts.ResetKey(id.email)
		acct, _ = s.accounts.LookupEmail(id.email)
	} else {
		// #386: the provider-supplied email goes through the same shared
		// validator as register/admin - an OAuth profile must not mint an
		// account the admin API would reject.
		acct, err = s.createAccountChecked(id.email, "free")
		if err != nil {
			s.logf("auth: %s account create refused: %v", provider, err)
			http.Error(w, "account create failed", http.StatusUnprocessableEntity)
			return
		}
		s.accounts.Mutate(id.email, func(a *Account) {
			a.Provider, a.Subject, a.Name = provider, id.subject, id.name
		})
	}
	http.SetCookie(w, s.sessionCookie(acct.Email))
	// Resume the pre-login hop (e.g. the CLI grant page) when the login
	// page was entered with return_to: strictly same-site relative paths,
	// so a crafted return_to can never bounce the user off this origin.
	if c, err := r.Cookie("et_returnto"); err == nil && c.Value != "" {
		v := c.Value
		http.SetCookie(w, &http.Cookie{Name: "et_returnto", Value: "", Path: "/", MaxAge: -1})
		if validReturnTo(v) {
			http.Redirect(w, r, v, http.StatusSeeOther)
			return
		}
	}
	http.Redirect(w, r, "/me", http.StatusSeeOther)
}

type oauthIdentityRes struct {
	subject, email, name string
	// emailVerified reports whether the provider verified the email (or
	// synthesized a per-subject noreply address that cannot be squatted).
	emailVerified bool
}

func (s *Server) oauthIdentity(ctx context.Context, provider, code string) (*oauthIdentityRes, error) {
	switch provider {
	case "google":
		tok, err := s.oauthToken(ctx, "https://oauth2.googleapis.com/token", url.Values{
			"client_id": {s.auth.googleID}, "client_secret": {s.auth.googleSecret},
			"code": {code}, "grant_type": {"authorization_code"},
			"redirect_uri": {s.billing.PublicBaseURL + "/auth/google/callback"},
		})
		if err != nil {
			return nil, err
		}
		var ui struct {
			Sub, Email, Name string
			EmailVerified    bool `json:"email_verified"`
		}
		if err := oauthGetJSON(ctx, "https://openidconnect.googleapis.com/v1/userinfo",
			tok["access_token"], &ui); err != nil {
			return nil, err
		}
		return &oauthIdentityRes{ui.Sub, strings.ToLower(ui.Email), ui.Name, ui.EmailVerified}, nil
	case "gitlab":
		tok, err := s.oauthToken(ctx, s.auth.gitlabURL+"/oauth/token", url.Values{
			"client_id": {s.auth.gitlabID}, "client_secret": {s.auth.gitlabSecret},
			"code": {code}, "grant_type": {"authorization_code"},
			"redirect_uri": {s.billing.PublicBaseURL + "/auth/gitlab/callback"},
		})
		if err != nil {
			return nil, err
		}
		var ui struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
			Email    string `json:"email"`
			Name     string `json:"name"`
		}
		if err := oauthGetJSON(ctx, s.auth.gitlabURL+"/api/v4/user", tok["access_token"], &ui); err != nil {
			return nil, err
		}
		email := strings.ToLower(ui.Email)
		verified := false
		if email == "" {
			email = fmt.Sprintf("%s@users.noreply.gitlab.com", ui.Username)
			// Provider-synthesized address: unique to this GitLab account and
			// not typeable by others, so it cannot be squatted for a claim.
			verified = true
		} else {
			// #215: /api/v4/user carries no confirmation flag; confirmation
			// state lives on /api/v4/user/emails (confirmed_at). Fail closed
			// when the listing is unavailable or shows no confirmed match.
			var glEmails []struct {
				Email       string `json:"email"`
				ConfirmedAt any    `json:"confirmed_at"`
			}
			if err := oauthGetJSON(ctx, s.auth.gitlabURL+"/api/v4/user/emails", tok["access_token"], &glEmails); err == nil {
				for _, e := range glEmails {
					if strings.ToLower(e.Email) == email && e.ConfirmedAt != nil {
						verified = true
						break
					}
				}
			}
		}
		return &oauthIdentityRes{fmt.Sprint(ui.ID), email, ui.Name, verified}, nil
	case "github":
		tok, err := s.oauthToken(ctx, "https://github.com/login/oauth/access_token", url.Values{
			"client_id": {s.auth.githubID}, "client_secret": {s.auth.githubSecret},
			"code": {code}, "redirect_uri": {s.billing.PublicBaseURL + "/auth/github/callback"},
		})
		if err != nil {
			return nil, err
		}
		var gh struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
			Name  string `json:"name"`
			Email string `json:"email"`
		}
		if err := oauthGetJSON(ctx, "https://api.github.com/user", tok["access_token"], &gh); err != nil {
			return nil, err
		}
		ghEmail := strings.ToLower(gh.Email)
		if ghEmail == "" {
			var emails []struct {
				Email    string `json:"email"`
				Primary  bool   `json:"primary"`
				Verified bool   `json:"verified"`
			}
			if err := oauthGetJSON(ctx, "https://api.github.com/user/emails", tok["access_token"], &emails); err == nil {
				for _, e := range emails {
					if e.Primary && e.Verified {
						ghEmail = strings.ToLower(e.Email)
						break
					}
				}
			}
		}
		if ghEmail == "" {
			ghEmail = fmt.Sprintf("%s@users.noreply.github.com", gh.Login)
		}
		ghName := gh.Name
		if ghName == "" {
			ghName = gh.Login
		}
		// GitHub emails are trusted as verified here: the /user field is the
		// confirmed primary address, the /user/emails fallback filters on
		// verified, and the noreply fallback is provider-synthesized.
		return &oauthIdentityRes{fmt.Sprint(gh.ID), ghEmail, ghName, true}, nil
	case "apple":
		tok, err := s.oauthToken(ctx, "https://appleid.apple.com/auth/token", url.Values{
			"client_id": {s.auth.appleID}, "client_secret": {s.appleClientSecret()},
			"code": {code}, "grant_type": {"authorization_code"},
			"redirect_uri": {s.billing.PublicBaseURL + "/auth/apple/callback"},
		})
		if err != nil {
			return nil, err
		}
		idt, err := decodeJWTClaims(tok["id_token"])
		if err != nil {
			return nil, err
		}
		sub, _ := idt["sub"].(string)
		email, _ := idt["email"].(string)
		email = strings.ToLower(email)
		// email_verified arrives as bool in newer tokens, "true"/"false"
		// strings in older ones.
		ev := false
		switch v := idt["email_verified"].(type) {
		case bool:
			ev = v
		case string:
			ev = v == "true"
		}
		if email == "" {
			// Apple only includes the email on the FIRST authorization; later
			// logins carry just the stable subject. Synthesize a per-subject
			// noreply address (unique, not typeable by others) so returning
			// users still pass the verified gate via provider+subject instead
			// of being locked out (#215).
			return &oauthIdentityRes{sub,
				fmt.Sprintf("%s@users.noreply.apple.com", sub), "", true}, nil
		}
		return &oauthIdentityRes{sub, email, "", ev}, nil
	}
	return nil, errors.New("unknown provider")
}

func (s *Server) oauthToken(ctx context.Context, endpoint string, form url.Values) (map[string]string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// GitHub defaults to form-encoded; ask for JSON everywhere.
	req.Header.Set("Accept", "application/json")
	resp, err := oauthHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	res := map[string]string{}
	// Try JSON first, then application/x-www-form-urlencoded (GitHub, some others).
	if json.Unmarshal(raw, &res) != nil {
		vals, ferr := url.ParseQuery(string(raw))
		if ferr != nil {
			return nil, fmt.Errorf("token endpoint %s (unparseable body)", resp.Status)
		}
		for k := range vals {
			res[k] = vals.Get(k)
		}
	}
	if resp.StatusCode >= 300 {
		if e := res["error_description"]; e != "" {
			return nil, fmt.Errorf("token endpoint %s: %s", resp.Status, e)
		}
		if e := res["error"]; e != "" {
			return nil, fmt.Errorf("token endpoint %s: %s", resp.Status, e)
		}
		return nil, fmt.Errorf("token endpoint %s", resp.Status)
	}
	if res["access_token"] == "" && res["id_token"] == "" {
		return nil, errors.New("no token in response")
	}
	return res, nil
}

func oauthGetJSON(ctx context.Context, u, bearer string, into any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := oauthHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("userinfo %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// decodeJWTClaims parses the unverified claims payload of a JWT. Apple's
// id_token is verified server-to-server via the token endpoint exchange
// above; claims here are only read after that exchange succeeded.
func decodeJWTClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("bad jwt")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// appleClientSecret builds the ES256-signed client-secret JWT Apple requires.
func (s *Server) appleClientSecret() string {
	now := time.Now()
	header := b64json(map[string]string{"alg": "ES256", "kid": s.auth.appleKey})
	claims := b64json(map[string]any{
		"iss": s.auth.appleTeam,
		"iat": now.Unix(),
		"exp": now.Add(30 * 24 * time.Hour).Unix(),
		"aud": "https://appleid.apple.com",
		"sub": s.auth.appleID,
	})
	signing := header + "." + claims
	key := parseAppleKey(s.auth.appleKeyPEM)
	if key == nil {
		s.logf("apple: private key unparseable (pemLen=%d, hasNewline=%v, team=%q kid=%q sub=%q)",
			len(s.auth.appleKeyPEM), strings.Contains(s.auth.appleKeyPEM, "\n"),
			s.auth.appleTeam, s.auth.appleKey, s.auth.appleID)
		return ""
	}
	h := sha256.Sum256([]byte(signing))
	r, ss, err := ecdsa.Sign(rand.Reader, key, h[:])
	if err != nil {
		return ""
	}
	rb, sb := r.Bytes(), ss.Bytes()
	sig := make([]byte, 64)
	copy(sig[32-len(rb):32], rb)
	copy(sig[64-len(sb):], sb)
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func parseAppleKey(pemStr string) *ecdsa.PrivateKey {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil
	}
	return ec
}

func b64json(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

var _ = io.Discard

// loadPEMValue returns PEM text from either a file path (leading "/",
// recommended: systemd env files do not decode escapes) or an inline value
// whose literal "\n" sequences are converted to real newlines.
func loadPEMValue(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "/") {
		b, err := os.ReadFile(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
	return strings.ReplaceAll(v, `\n`, "\n")
}
