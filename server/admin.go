package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// registerLimiter is a tiny per-IP limiter for the public register endpoint.
type registerLimiter struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

var regLimiter = &registerLimiter{seen: make(map[string]time.Time)}

// regLimiterFor returns this server's throttle bucket, falling back to the
// package default for hand-built Servers (tests) so the endpoint never runs
// unthrottled.
func (s *Server) regLimiterFor() *registerLimiter {
	if s.regLimiter != nil {
		return s.regLimiter
	}
	return regLimiter
}

func (l *registerLimiter) allow(ip string, every time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if t, ok := l.seen[ip]; ok && now.Sub(t) < every {
		return false
	}
	l.seen[ip] = now
	if len(l.seen) > 10000 {
		l.seen = make(map[string]time.Time)
	}
	return true
}

func (s *Server) buildAdmin() {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/stats", s.adminAuthOptional(s.handleStats))
	mux.HandleFunc("/admin/accounts", s.adminAuth(s.handleAdminAccounts))
	mux.HandleFunc("/admin/accounts/", s.adminAuth(s.handleAdminAccountDelete))
	s.adminHandler = mux
}

func (s *Server) adminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Fail closed: an unconfigured AdminToken disables the admin API
		// entirely (previously any caller passed when the token was unset).
		if s.cfg.AdminToken == "" {
			http.Error(w, "admin API disabled: set -admin-token", http.StatusServiceUnavailable)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Admin-Token")), []byte(s.cfg.AdminToken)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// adminAuthOptional guards read-only ops routes (/stats): it enforces the
// token when one is configured, but stays open when AdminToken is unset so
// local `curl 127.0.0.1:9090/stats` keeps working. Mutating admin routes use
// adminAuth, which fails closed.
func (s *Server) adminAuthOptional(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AdminToken == "" {
			next(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Admin-Token")), []byte(s.cfg.AdminToken)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

type tunnelStat struct {
	Subdomain string `json:"subdomain"`
	Hostname  string `json:"hostname"`
	URL       string `json:"url"`
	Plan      string `json:"plan"`
	Custom    bool   `json:"custom"`
	Account   string `json:"account,omitempty"`
	RemoteIP  string `json:"remoteIp"`
	Streams   int    `json:"streams"`
	ExpiresAt int64  `json:"expiresAt,omitempty"`
}

func (s *Server) handleStats(w http.ResponseWriter, _ *http.Request) {
	sessions := s.reg.Snapshot()
	tunnels := make([]tunnelStat, 0, len(sessions))
	for _, sess := range sessions {
		acct := ""
		if sess.account != nil {
			acct = sess.account.Email
		}
		tunnels = append(tunnels, tunnelStat{
			Subdomain: sess.sub,
			Hostname:  sess.hostname,
			URL:       sess.url,
			Plan:      sess.tier.Name,
			Custom:    sess.custom,
			Account:   acct,
			RemoteIP:  sess.ip,
			Streams:   sess.streamCount(),
			ExpiresAt: expiresUnix(sess.expires),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{

		"billing": map[string]any{
			"stripe":    s.billing.StripeSecretKey != "",
			"paypal":    s.billing.PayPalClientID != "",
			"paypalEnv": s.billing.PayPalEnv,
		}, "uptime": time.Since(s.metrics.startedAt).Round(time.Second).String(),
		"activeTunnels": s.reg.Count(),
		"tunnelsTotal":  s.metrics.tunnelsTotal.Load(),
		"requestsTotal": s.metrics.requestsTotal.Load(),
		"bytesIn":       s.metrics.bytesIn.Load(),
		"bytesOut":      s.metrics.bytesOut.Load(),
		"tunnels":       tunnels,
	})
}

func (s *Server) handleAdminAccounts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		type acct struct {
			Email   string    `json:"email"`
			Tier    string    `json:"tier"`
			Created time.Time `json:"created"`
		}
		out := make([]acct, 0)
		for _, a := range s.accounts.List() {
			out = append(out, acct{Email: a.Email, Tier: a.Tier, Created: a.Created})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	case http.MethodPost:
		var req struct {
			Email string `json:"email"`
			Tier  string `json:"tier"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if req.Tier == "" {
			req.Tier = s.cfg.DefaultPaidTier
		}
		if _, ok := s.cfg.Tiers[req.Tier]; !ok {
			http.Error(w, "unknown tier", http.StatusBadRequest)
			return
		}
		a, err := s.accounts.Create(req.Email, req.Tier)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"apiKey": a.APIKey, "tier": a.Tier})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAdminAccountDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/admin/accounts/")
	if !s.accounts.Delete(key) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- public registration API ---------------------------------------------------

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ip := remoteIP(r)
	if !s.regLimiterFor().allow(ip, time.Minute) {
		http.Error(w, "too many registrations, retry later", http.StatusTooManyRequests)
		return
	}
	var req struct {
		Email string `json:"email"`
		Tier  string `json:"tier"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email)) // match the OAuth paths: Bob@x.com == bob@x.com
	if !strings.Contains(req.Email, "@") || len(req.Email) > 254 {
		http.Error(w, "invalid email", http.StatusBadRequest)
		return
	}
	// SECURITY: /api/register is public and unauthenticated. It may only mint
	// free-tier accounts - paid tiers must go through the billing checkout
	// (/api/billing/checkout), which verifies payment before activation.
	if req.Tier != "" && req.Tier != "free" {
		http.Error(w, `{"error":"paid tiers require checkout","hint":"subscribe at /pricing"}`, http.StatusPaymentRequired)
		return
	}
	req.Tier = "free"
	tier, ok := s.cfg.Tiers[req.Tier]
	if !ok {
		http.Error(w, "unknown tier", http.StatusBadRequest)
		return
	}
	a, err := s.accounts.Create(req.Email, req.Tier)
	if err != nil {
		if errors.Is(err, ErrEmailTaken) {
			http.Error(w, "email already registered", http.StatusConflict)
			return
		}
		s.logf("register: create account: %v", err)
		http.Error(w, "registration failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"apiKey": a.APIKey,
		"email":  a.Email,
		"tier":   a.Tier,
		"plan": map[string]any{
			"name":          tier.Name,
			"ttlHours":      int(tier.TTL.Hours()),
			"customDomains": tier.CustomDomains,
			"bandwidthMbps": tier.Bandwidth / 1_000_000,
		},
	})
}

func (s *Server) handleTiers(w http.ResponseWriter, _ *http.Request) {
	out := make([]map[string]any, 0, len(s.cfg.Tiers))
	for id, t := range s.cfg.Tiers {
		out = append(out, map[string]any{
			"id":            id,
			"name":          t.Name,
			"ttlHours":      int(t.TTL.Hours()),
			"customDomains": t.CustomDomains,
			"bandwidthMbps": t.Bandwidth / 1_000_000,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
