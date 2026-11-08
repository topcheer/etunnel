package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
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
	cfg          Config
	reg          *Registry
	accounts     *AccountStore
	metrics      *metrics
	adminMux     *http.ServeMux
	handler      http.Handler
	adminHandler http.Handler
	billing      BillingConfig
	tg           telegramConfig
	auth         oauthConfig
}

// logf emits a server log line (stdout + journald under systemd).
func (s *Server) logf(format string, args ...any) {
	fmt.Printf("etunneld: "+format+"\n", args...)
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
		cfg:      cfg,
		reg:      newRegistry(),
		accounts: accounts,
		metrics:  &metrics{startedAt: time.Now()},
	}

	mux := http.NewServeMux()
	mux.HandleFunc(cfg.WSPath, s.handleTunnelWS)
	mux.HandleFunc("/api/register", s.handleRegister)
	mux.HandleFunc("/api/tiers", s.handleTiers)
	mux.HandleFunc("/pricing", s.handlePricing)
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/me", s.handleMe)
	mux.HandleFunc("/me/", func(w http.ResponseWriter, r *http.Request) {
		s.handleMeAction(w, r, strings.TrimPrefix(r.URL.Path, "/me/"))
	})
	for _, p := range []string{"google", "gitlab", "apple"} {
		mux.HandleFunc("/auth/"+p+"/login", func(w http.ResponseWriter, r *http.Request) { s.handleOAuthLogin(w, r, p) })
		mux.HandleFunc("/auth/"+p+"/callback", func(w http.ResponseWriter, r *http.Request) { s.handleOAuthCallback(w, r, p) })
	}
	mux.HandleFunc("/api/billing/checkout", s.handleBillingCheckout)
	mux.HandleFunc("/billing/success", s.handleBillingSuccess)
	mux.HandleFunc("/billing/paypal/return", s.handlePayPalReturn)
	mux.HandleFunc("/webhooks/stripe", s.handleStripeWebhook)
	mux.HandleFunc("/webhooks/paypal", s.handlePayPalWebhook)
	mux.HandleFunc("/", s.handleProxy)
	s.handler = mux
	s.buildAdmin()
	s.billing = billingFromEnv(cfg.Domain)
	s.auth = oauthFromEnv()
	return s, nil
}

// Handler returns the main HTTP handler (useful for tests and embedding).
func (s *Server) Handler() http.Handler { return s.handler }

// Accounts exposes the account store (for tests/tools).
func (s *Server) Accounts() *AccountStore { return s.accounts }

// Run starts all listeners and blocks until ctx is done.
func (s *Server) Run(ctx context.Context) error {
	var servers []*http.Server
	s.startTelegram(ctx)
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
			httpHandler = redirectHandler(tlsConf.httpChallenge)
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

func redirectHandler(challenge http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if challenge != nil && strings.HasPrefix(r.URL.Path, "/.well-known/acme-challenge/") {
			challenge.ServeHTTP(w, r)
			return
		}
		target := "https://" + r.Host + r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})
}
