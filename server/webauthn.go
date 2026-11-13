// Passkey (WebAuthn) support: users who are logged in (via OAuth or an
// existing session) can register one or more passkeys - platform
// authenticators (Touch ID, Windows Hello) or roaming keys (YubiKey) - and
// then sign in from the login page without any provider. The session minted
// by a successful passkey login is the same "et_session" cookie OAuth uses,
// so every downstream surface (portal, CLI grant, billing) works unchanged.
//
// Storage: passkeys live on the Account (JSON store: automatic; PG: the
// passkeys jsonb column). Credential data is the decoded protocol.Credential,
// trimmed to what validation needs plus a user-chosen device name.
//
// Ceremony session state is per-instance in-memory (5-minute TTL). The
// production topology is a single node; a multi-node cluster would need the
// begin/finish pair pinned to one node (sticky routing) or a shared store.
package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// Passkey is one stored WebAuthn credential on an account.
type Passkey struct {
	ID       []byte    `json:"id"` // credential ID (raw)
	PubKey   []byte    `json:"pk"` // COSE public key
	Attest   string    `json:"at"` // attestation statement format
	AAGUID   []byte    `json:"aa"` // authenticator model id (empty for none)
	SignCnt  uint32    `json:"sc"` // signature counter (clone detection)
	BE       bool      `json:"be"` // backup eligible
	BS       bool      `json:"bs"` // backed up
	Name     string    `json:"nm"` // user-chosen device name
	Created  time.Time `json:"ct"`
	LastUsed time.Time `json:"lu,omitempty"`
}

// waUser adapts an Account to the webauthn.User interface. The stable user
// handle is the account email - discoverable credentials return it, which is
// how username-less login resolves the account.
type waUser struct{ a *Account }

func (u waUser) WebAuthnID() []byte   { return []byte(u.a.Email) }
func (u waUser) WebAuthnName() string { return u.a.Email }
func (u waUser) WebAuthnDisplayName() string {
	if u.a.Name != "" {
		return u.a.Name
	}
	return u.a.Email
}
func (u waUser) WebAuthnCredentials() []webauthn.Credential {
	out := make([]webauthn.Credential, 0, len(u.a.Passkeys))
	for _, p := range u.a.Passkeys {
		out = append(out, webauthn.Credential{
			ID:              p.ID,
			PublicKey:       p.PubKey,
			AttestationType: p.Attest,
			Authenticator:   webauthn.Authenticator{AAGUID: p.AAGUID, SignCount: p.SignCnt, CloneWarning: false},
			Flags:           webauthn.CredentialFlags{BackupEligible: p.BE, BackupState: p.BS},
		})
	}
	return out
}

// waSessions holds in-flight ceremony state keyed by an opaque cookie value.
type waSessions struct {
	mu    sync.Mutex
	m     map[string]*webauthn.SessionData
	email map[string]string // session id -> account email (registration only)
	exp   map[string]time.Time
}

func newWaSessions() *waSessions {
	return &waSessions{m: map[string]*webauthn.SessionData{}, email: map[string]string{}, exp: map[string]time.Time{}}
}

func (w *waSessions) put(id string, sd *webauthn.SessionData, email string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	for k, e := range w.exp { // lazy prune
		if now.After(e) {
			delete(w.m, k)
			delete(w.email, k)
			delete(w.exp, k)
		}
	}
	w.m[id], w.email[id], w.exp[id] = sd, email, now.Add(5*time.Minute)
}

func (w *waSessions) take(id string) (*webauthn.SessionData, string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	sd, ok := w.m[id]
	if !ok {
		return nil, "", false
	}
	em := w.email[id]
	delete(w.m, id)
	delete(w.email, id)
	delete(w.exp, id)
	return sd, em, true
}

// wa returns the per-Server WebAuthn instance, created on first use.
func (s *Server) wa() *webauthn.WebAuthn {
	s.waOnce.Do(func() {
		rpID := s.cfg.Domain
		if h, _, err := net.SplitHostPort(rpID); err == nil {
			rpID = h
		}
		scheme := "https"
		if s.cfg.TLS.Mode == TLSModeHTTP {
			scheme = "http"
		}
		s.waInst, _ = webauthn.New(&webauthn.Config{
			RPID:          rpID,
			RPDisplayName: "etunnel",
			RPOrigins:     []string{scheme + "://" + s.cfg.Domain},
		})
	})
	return s.waInst
}

// net_SplitHostPort was replaced by net.SplitHostPort.

func waCookie() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) waSetCookie(w http.ResponseWriter, id string) {
	http.SetCookie(w, &http.Cookie{Name: "et_pkflow", Value: id, Path: "/auth/passkey", MaxAge: 300, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: true})
}
func (s *Server) waFlowID(r *http.Request) string {
	c, err := r.Cookie("et_pkflow")
	if err != nil {
		return ""
	}
	return c.Value
}

// handlePasskeyRegisterBegin starts a credential creation ceremony for the
// signed-in user. GET /auth/passkey/register/begin
func (s *Server) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	email := s.sessionEmail(r)
	if email == "" {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	acct, ok := s.accounts.LookupEmail(email)
	if !ok {
		http.Error(w, "account not found", http.StatusUnauthorized)
		return
	}
	inst := s.wa()
	if inst == nil {
		http.Error(w, "passkey not configured", http.StatusServiceUnavailable)
		return
	}
	opts, sd, err := inst.BeginRegistration(waUser{acct},
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired, // discoverable: enables username-less login
			UserVerification: protocol.VerificationPreferred,
		}),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
	)
	if err != nil {
		http.Error(w, "begin: "+err.Error(), http.StatusBadRequest)
		return
	}
	id := waCookie()
	s.waFlow.put(id, sd, email)
	s.waSetCookie(w, id)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(opts.Response)
}

// handlePasskeyRegisterFinish verifies the attestation and stores the
// credential. POST /auth/passkey/register/finish?name=<device name>
// Body: the browser PublicKeyCredential serialized as JSON.
func (s *Server) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	email := s.sessionEmail(r)
	if email == "" {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	sd, flowEmail, ok := s.waFlow.take(s.waFlowID(r))
	if !ok || flowEmail != email {
		http.Error(w, "registration session expired - try again", http.StatusBadRequest)
		return
	}
	acct, ok := s.accounts.LookupEmail(email)
	if !ok {
		http.Error(w, "account not found", http.StatusUnauthorized)
		return
	}
	cred, err := s.wa().FinishRegistration(waUser{acct}, *sd, r)
	if err != nil {
		http.Error(w, "finish: "+err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = "passkey"
	}
	if len(name) > 60 {
		name = name[:60]
	}
	s.accounts.Mutate(email, func(a *Account) {
		// Upsert by credential ID: re-registering the same authenticator
		// refreshes its key instead of piling up duplicates.
		for i := range a.Passkeys {
			if string(a.Passkeys[i].ID) == string(cred.ID) {
				a.Passkeys[i] = Passkey{ID: cred.ID, PubKey: cred.PublicKey, Attest: cred.AttestationType, AAGUID: cred.Authenticator.AAGUID, SignCnt: cred.Authenticator.SignCount, BE: cred.Flags.BackupEligible, BS: cred.Flags.BackupState, Name: name, Created: time.Now().UTC()}
				return
			}
		}
		a.Passkeys = append(a.Passkeys, Passkey{ID: cred.ID, PubKey: cred.PublicKey, Attest: cred.AttestationType, AAGUID: cred.Authenticator.AAGUID, SignCnt: cred.Authenticator.SignCount, BE: cred.Flags.BackupEligible, BS: cred.Flags.BackupState, Name: name, Created: time.Now().UTC()})
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// handlePasskeyLoginBegin starts a discoverable (username-less) assertion.
// GET /auth/passkey/login/begin
func (s *Server) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	inst := s.wa()
	if inst == nil {
		http.Error(w, "passkey not configured", http.StatusServiceUnavailable)
		return
	}
	opts, sd, err := inst.BeginDiscoverableLogin()
	if err != nil {
		http.Error(w, "begin: "+err.Error(), http.StatusBadRequest)
		return
	}
	id := waCookie()
	s.waFlow.put(id, sd, "")
	s.waSetCookie(w, id)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(opts.Response)
}

// handlePasskeyLoginFinish verifies the assertion, resolves the account from
// the discoverable credential's user handle, and mints the standard session.
// POST /auth/passkey/login/finish   Body: PublicKeyCredential JSON.
func (s *Server) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	sd, _, ok := s.waFlow.take(s.waFlowID(r))
	if !ok {
		http.Error(w, "login session expired - try again", http.StatusBadRequest)
		return
	}
	var loginEmail string
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		loginEmail = string(userHandle)
		acct, found := s.accounts.LookupEmail(loginEmail)
		if !found {
			return nil, fmt.Errorf("unknown account")
		}
		return waUser{acct}, nil
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(r.Body)
	if err != nil {
		http.Error(w, "bad credential response", http.StatusBadRequest)
		return
	}
	cred, err := s.wa().ValidateDiscoverableLogin(handler, *sd, parsed)
	if err != nil {
		http.Error(w, "finish: "+err.Error(), http.StatusUnauthorized)
		return
	}
	if loginEmail == "" {
		http.Error(w, "no credential returned", http.StatusUnauthorized)
		return
	}
	acct, found := s.accounts.LookupEmail(loginEmail)
	if !found {
		http.Error(w, "account not found", http.StatusUnauthorized)
		return
	}
	s.accounts.Mutate(acct.Email, func(a *Account) {
		for i := range a.Passkeys {
			if string(a.Passkeys[i].ID) == string(cred.ID) {
				a.Passkeys[i].SignCnt = cred.Authenticator.SignCount
				a.Passkeys[i].LastUsed = time.Now().UTC()
			}
		}
	})
	http.SetCookie(w, s.sessionCookie(acct.Email))
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true,"email":` + quoteJSON(acct.Email) + `}`))
}

func (s *Server) accountByCredentialID(id []byte) (*Account, bool) {
	for _, a := range s.accounts.List() {
		for _, p := range a.Passkeys {
			if string(p.ID) == string(id) {
				return a, true
			}
		}
	}
	return nil, false
}

// handlePasskeyList lists the signed-in user's passkeys (for the /me panel).
func (s *Server) handlePasskeyList(w http.ResponseWriter, r *http.Request) {
	email := s.sessionEmail(r)
	if email == "" {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	acct, ok := s.accounts.LookupEmail(email)
	if !ok {
		http.Error(w, "account not found", http.StatusUnauthorized)
		return
	}
	type row struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Created  string `json:"created"`
		LastUsed string `json:"lastUsed,omitempty"`
	}
	out := make([]row, 0, len(acct.Passkeys))
	for _, p := range acct.Passkeys {
		out = append(out, row{ID: base64.RawURLEncoding.EncodeToString(p.ID), Name: p.Name, Created: p.Created.Format("2006-01-02"), LastUsed: p.LastUsed.Format("2006-01-02")})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handlePasskeyRemove deletes one passkey by base64url credential ID.
// POST /auth/passkey/remove?id=<b64url>
func (s *Server) handlePasskeyRemove(w http.ResponseWriter, r *http.Request) {
	email := s.sessionEmail(r)
	if email == "" {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	idStr := r.FormValue("id")
	raw, err := base64.RawURLEncoding.DecodeString(idStr)
	if err != nil || len(raw) == 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	removed := false
	s.accounts.Mutate(email, func(a *Account) {
		out := a.Passkeys[:0]
		for _, p := range a.Passkeys {
			if string(p.ID) == string(raw) {
				removed = true
				continue
			}
			out = append(out, p)
		}
		a.Passkeys = out
	})
	if !removed {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func quoteJSON(s string) string { b, _ := json.Marshal(s); return string(b) }
