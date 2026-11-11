package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/topcheer/etunnel/protocol"
)

type metrics struct {
	tunnelsTotal  atomic.Int64
	requestsTotal atomic.Int64
	bytesIn       atomic.Int64
	bytesOut      atomic.Int64
	startedAt     time.Time
}

// Server is an etunnel tunnel server.
type Server struct {
	cfg      Config
	reg      *Registry
	accounts accountBackend
	metrics  *metrics
	// regLimiter throttles /api/register per source IP. Per instance rather
	// than process-global: two Servers in one process (tests, embedders) must
	// not share one bucket, which made repeated runs fail with 429.
	regLimiter   *registerLimiter
	adminMux     *http.ServeMux
	handler      http.Handler
	adminHandler http.Handler
	billing      BillingConfig
	tg           telegramConfig
	auth         oauthConfig
	counter      Counter        // per-IP / per-account tunnel limits (mem or redis)
	ledger       *sessionLedger // session audit rows in PG (nil = journal-only, #69)
	router       Router         // cluster routing (nil = single node)
	draining     atomic.Bool
}

// logf emits a server log line (stdout + journald under systemd).
// logPrintf is a var so tests can capture the output (#42).
var logPrintf = fmt.Printf

func (s *Server) logf(format string, args ...any) {
	logPrintf("etunneld: "+format+"\n", args...)
}

// New builds a Server from cfg.
func New(cfg Config) (*Server, error) {
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	accounts, err := NewAccountStore(cfg.AccountsFile)
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:        cfg,
		reg:        newRegistry(),
		accounts:   accounts,
		metrics:    &metrics{startedAt: time.Now()},
		regLimiter: &registerLimiter{seen: make(map[string]time.Time)},
	}

	mux := http.NewServeMux()
	mux.HandleFunc(cfg.WSPath, s.handleTunnelWS)
	mux.HandleFunc("/api/register", s.handleRegister)
	mux.HandleFunc("/api/tiers", s.handleTiers)
	mux.HandleFunc("/healthz", s.HealthzHandler().ServeHTTP)
	mux.HandleFunc("/pricing", s.handlePricing)
	mux.HandleFunc("/terms", s.handleTerms)
	mux.HandleFunc("/privacy", s.handlePrivacy)
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/logout", s.handleLogout)
	mux.HandleFunc("/me", s.handleMe)
	mux.HandleFunc("/me/", func(w http.ResponseWriter, r *http.Request) {
		s.handleMeAction(w, r, strings.TrimPrefix(r.URL.Path, "/me/"))
	})
	for _, p := range []string{"google", "gitlab", "github", "apple"} {
		mux.HandleFunc("/auth/"+p+"/login", func(w http.ResponseWriter, r *http.Request) { s.handleOAuthLogin(w, r, p) })
		mux.HandleFunc("/auth/"+p+"/callback", func(w http.ResponseWriter, r *http.Request) { s.handleOAuthCallback(w, r, p) })
	}
	mux.HandleFunc("/api/billing/checkout", s.handleBillingCheckout)
	mux.HandleFunc("/billing/success", s.handleBillingSuccess)
	mux.HandleFunc("/billing/paypal/return", s.handlePayPalReturn)
	mux.HandleFunc("/webhooks/stripe", s.handleStripeWebhook)
	mux.HandleFunc("/webhooks/paypal", s.handlePayPalWebhook)
	mux.HandleFunc("/webhooks/nowpayments", s.handleNowPaymentsIPN)
	mux.HandleFunc("/billing/crypto/success", s.handleCryptoSuccess)
	// Serve bundled brand assets (hero video etc.): local ./assets first,
	// then the server install dir. Host-scoped to the base domain only:
	// a tunnel subdomain's own /assets/* must reach the tunneled service,
	// not this file server (ServeMux prefers /assets/ over the "/" catch-
	// all, which 404'd every tunneled WebUI asset with Go's default page).
	for _, dir := range []string{"assets", "/opt/etunnel/assets"} {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			brand := http.StripPrefix("/assets/", http.FileServer(http.Dir(dir)))
			mux.Handle("/assets/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, isBase, _ := s.hostLabel(r.Host); isBase {
					// #38: concrete files only - never a directory index. The
					// listing disclosed the bundle's file names.
					if strings.HasSuffix(r.URL.Path, "/") {
						http.NotFound(w, r)
						return
					}
					brand.ServeHTTP(w, r)
					return
				}
				s.handleProxy(w, r)
			}))
			break
		}
	}
	mux.HandleFunc("/", s.handleProxy)
	// accessLog outermost: it logs only base-domain requests and bypasses
	// upgrade requests untouched; routeByHost inside dispatches base hosts to
	// etunneld's own endpoints and everything else straight to the proxy.
	s.handler = s.accessLog(s.routeByHost(mux))
	s.buildAdmin()
	s.billing = billingFromEnv(cfg.Domain)
	s.auth = oauthFromEnv()
	return s, nil
}

// Handler returns the main HTTP handler (useful for tests and embedding).
func (s *Server) Handler() http.Handler { return s.handler }

// accessLog writes one line per request served on the base domain (#42):
// portal, OAuth, billing, webhook and asset traffic. Tunnel-subdomain
// proxying is deliberately quiet - it is the hot path and carries the
// tunnelled service's own traffic, not ours. Admin-auth failures are logged
// unconditionally in adminAuth regardless of host.
//
// Connection-upgrade requests (the SDK's wss://<base>/tunnel handshake in
// production) bypass the wrapper entirely: websocket.Accept needs the
// underlying ResponseWriter's Hijacker, and interface embedding does not
// promote it - wrapping returned 501 and dropped every base-domain SDK
// connection (PR #46 review).
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, isBase, _ := s.hostLabel(r.Host)
		if !isBase || isUpgradeRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.logf("http: %s %s %d %s", r.Method, r.URL.RequestURI(), rec.code, remoteIP(r))
	})
}

// isUpgradeRequest reports whether r asks to switch protocols (RFC 7230
// "Connection" is a comma-separated token list, e.g. "keep-alive, Upgrade").
func isUpgradeRequest(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("Upgrade"), "") {
		return false
	}
	for _, tok := range strings.Split(r.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
			return true
		}
	}
	return false
}

// statusRecorder captures the status code for access logging.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

// Accounts exposes the account store (for tests/tools).
func (s *Server) Accounts() accountBackend { return s.accounts }

// Run starts all listeners and blocks until ctx is done.
func (s *Server) Run(ctx context.Context) error {
	if cc, closers, err := setupCluster(ctx); err != nil {
		return err
	} else {
		s.applyCluster(ctx, cc, closers)
	}
	// Session ledger (#69 Tier 2): same DSN knob as the account store, but
	// independent of cluster mode - a single node with ETUNNEL_PG_DSN gets
	// session rows too. No DSN: ledger stays nil (nil-method no-ops) and the
	// journal lines are the only record.
	if dsn := os.Getenv("ETUNNEL_PG_DSN"); dsn != "" {
		lg, err := newSessionLedger(dsn)
		if err != nil {
			return err
		}
		s.ledger = lg
		defer lg.Close()
	}
	go func() {
		<-ctx.Done()
		s.Drain() // healthz flips to 503: LB removes us before sessions die
	}()
	var servers []*http.Server
	s.startTelegram(ctx)
	s.startTierReaper(ctx)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		for _, srv := range servers {
			_ = srv.Shutdown(shutdownCtx)
		}
		cancel()
	}()

	// --- TLS provider -------------------------------------------------------
	var tlsConf *tlsConfigBundle
	if s.cfg.TLS.Mode != TLSModeHTTP {
		var err error
		tlsConf, err = s.setupTLS(ctx)
		if err != nil {
			return fmt.Errorf("etunnel: tls setup: %w", err)
		}
	}

	// --- main listener (443) ------------------------------------------------
	mainLn, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("etunnel: listen %s: %w", s.cfg.Addr, err)
	}
	if tlsConf != nil {
		mainLn = newTLSListener(mainLn, tlsConf.tlsConfig)
	}
	mainSrv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	servers = append(servers, mainSrv)
	go func() { _ = mainSrv.Serve(mainLn) }()

	// --- plain HTTP listener (80) ---------------------------------------------
	if s.cfg.HTTPAddr != "" {
		httpLn, err := net.Listen("tcp", s.cfg.HTTPAddr)
		if err != nil {
			return fmt.Errorf("etunnel: listen %s: %w", s.cfg.HTTPAddr, err)
		}
		var httpHandler http.Handler
		if s.cfg.TLS.Mode == TLSModeHTTP {
			httpHandler = s.handler // dev mode: everything over plain HTTP
		} else {
			httpHandler = s.redirectHandler(tlsConf.httpChallenge)
		}
		httpSrv := &http.Server{
			Handler:           httpHandler,
			ReadHeaderTimeout: 15 * time.Second,
		}
		servers = append(servers, httpSrv)
		go func() { _ = httpSrv.Serve(httpLn) }()
	}

	// --- admin listener -------------------------------------------------------
	if s.cfg.AdminAddr != "" {
		adminLn, err := net.Listen("tcp", s.cfg.AdminAddr)
		if err != nil {
			return fmt.Errorf("etunnel: listen %s: %w", s.cfg.AdminAddr, err)
		}
		adminSrv := &http.Server{Handler: s.adminHandler}
		servers = append(servers, adminSrv)
		go func() { _ = adminSrv.Serve(adminLn) }()
	}

	<-ctx.Done()
	// Ask live tunnels to hang up politely.
	for _, sess := range s.reg.Snapshot() {
		sess.Close(protocol.ErrCodeServerShutdown)
	}
	return nil
}

// routeByHost splits traffic at the host level (#35, #41): etunneld's own
// endpoints (login, portal, API, webhooks, the tunnel websocket) are served
// only on the base domain; every other host - a tunnel subdomain or a host we
// do not recognise at all - goes straight to the proxy. Go's ServeMux matches
// exact patterns before the "/" catch-all, so without this the reserved
// paths shadow the same routes on a user's tunnel, silently breaking user
// WebUIs and rendering our login page on other people's domains.
//
// The /tunnel websocket yields too, by design: SDK clients dial the base
// domain (wss://<domain>/tunnel), so a subdomain's /tunnel belongs to the
// user's own service.
//
// Base-host responses also get the security headers (#41) here - and ONLY
// here, on responses etunneld itself generates; proxied bytes pass through
// untouched.
func (s *Server) routeByHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, isBase, _ := s.hostLabel(r.Host); isBase {
			secureHeaders(w)
			next.ServeHTTP(w, r)
			return
		}
		s.handleProxy(w, r)
	})
}

// secureHeaders is the baseline hardening for pages etunneld renders (#41).
// HSTS deliberately omits includeSubDomains: the wildcard subdomains are
// tenants, and a tenant serving something other than browser HTTPS must not
// inherit our TLS policy. The CSP is frame-ancestors only because the portal
// uses inline styles and scripts; a default-src policy would break it.
func secureHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Strict-Transport-Security", "max-age=31536000")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "frame-ancestors 'none'")
}

// redirectHandler moves plain-HTTP traffic to HTTPS (#37). The Location host
// is never taken from the request verbatim: base hosts redirect to the
// canonical domain, tunnel subdomains (which are ours) keep their host, and
// any other Host gets 421 Misdirected Request - previously a spoofed Host was
// reflected into a valid-looking https:// URL. The ACME challenge path stays
// first and answers for any host, as issuance requires.
func (s *Server) redirectHandler(challenge http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if challenge != nil && strings.HasPrefix(r.URL.Path, "/.well-known/acme-challenge/") {
			challenge.ServeHTTP(w, r)
			return
		}
		host := r.Host
		if _, isBase, isSub := s.hostLabel(host); isBase {
			host = s.cfg.Domain
		} else if !isSub {
			http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
			return
		}
		target := "https://" + host + r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})
}
