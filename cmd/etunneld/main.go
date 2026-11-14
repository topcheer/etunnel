// Command etunneld runs an etunnel tunnel server.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/topcheer/etunnel/server"
)

func main() {
	var (
		domain     = flag.String("domain", "", "tunnel base domain, e.g. t.example.com (required)")
		aliasDom   = flag.String("alias-domain", "", "legacy alias domain served alongside -domain (static wildcard cert, for deprecation windows)")
		addr       = flag.String("addr", ":443", "main HTTPS listen address")
		httpAddr   = flag.String("http-addr", ":80", "plain HTTP listen address (redirect + ACME); empty disables")
		adminAddr  = flag.String("admin-addr", "127.0.0.1:9090", "admin/stats listen address")
		adminToken = flag.String("admin-token", os.Getenv("ADMIN_TOKEN"), "token required for admin endpoints (X-Admin-Token; env ADMIN_TOKEN)")
		trustFwd   = flag.Bool("trust-forwarded-for", func() bool { v := os.Getenv("TRUST_FORWARDED_FOR"); return v == "true" || v == "1" }(), "honour forwarded headers (CF-Connecting-IP/X-Real-IP/X-Forwarded-For) for policy allowIPs - only behind a trusted proxy that rewrites them (env TRUST_FORWARDED_FOR)")
		wsPath     = flag.String("ws-path", "/tunnel", "WebSocket endpoint path")

		tlsMode   = flag.String("tls-mode", "http", "http | static | ondemand | dns")
		certFile  = flag.String("tls-cert", "", "static mode: certificate file")
		keyFile   = flag.String("tls-key", "", "static mode: key file")
		acmeDir   = flag.String("acme-dir", "/var/lib/etunnel/acme", "ACME cache directory")
		acmeEmail = flag.String("acme-email", "", "ACME account email")
		acmeCA    = flag.String("acme-ca", "prod", "prod | staging | ACME directory URL")
		dnsProv   = flag.String("dns-provider", "", "dns mode: cloudflare | alidns | dnspod")

		accountsFile = flag.String("accounts", "/var/lib/etunnel/accounts.json", "accounts store file")
		requireToken = flag.Bool("require-token", false, "reject anonymous tunnels")
		maxPerIP     = flag.Int("max-tunnels-per-ip", 5, "max concurrent anonymous tunnels per IP (0=unlimited)")
		maxStreams   = flag.Int("max-streams", 256, "max concurrent streams per tunnel")
		tiersFile    = flag.String("tiers", "", "custom tiers JSON file")
		defaultTier  = flag.String("free-tier", "free", "tier for anonymous tunnels")
		defaultPaid  = flag.String("paid-tier", "starter", "tier for new registrations")
	)
	flag.Parse()

	cfg := server.Config{
		Domain:              *domain,
		AliasDomain:         *aliasDom,
		Addr:                *addr,
		HTTPAddr:            *httpAddr,
		AdminAddr:           *adminAddr,
		AdminToken:          *adminToken,
		TrustForwardedFor:   *trustFwd,
		WSPath:              *wsPath,
		TLS:                 server.TLSConfig{Mode: *tlsMode, CertFile: *certFile, KeyFile: *keyFile, CacheDir: *acmeDir, ACMEEmail: *acmeEmail, ACMECA: *acmeCA, DNSProvider: *dnsProv},
		AccountsFile:        *accountsFile,
		RequireToken:        *requireToken,
		MaxTunnelsPerIP:     *maxPerIP,
		MaxStreamsPerTunnel: *maxStreams,
		DefaultTier:         *defaultTier,
		DefaultPaidTier:     *defaultPaid,
	}

	if *tiersFile != "" {
		b, err := os.ReadFile(*tiersFile)
		if err != nil {
			log.Fatalf("read tiers file: %v", err)
		}
		var tf server.TierFile
		if err := json.Unmarshal(b, &tf); err != nil {
			log.Fatalf("parse tiers file: %v", err)
		}
		tiers := make(map[string]*server.Tier)
		for id, t := range tf {
			t := t
			tiers[id] = &t
		}
		cfg.Tiers = tiers
	} else {
		cfg.Tiers = server.DefaultTiers()
	}

	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("init: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		time.Sleep(200 * time.Millisecond)
		log.Printf("etunneld ready: domain=%s addr=%s http=%s admin=%s tls=%s",
			*domain, *addr, *httpAddr, *adminAddr, *tlsMode)
	}()

	if err := srv.Run(ctx); err != nil {
		log.Fatalf("run: %v", err)
	}
	fmt.Println("etunneld stopped")
}
