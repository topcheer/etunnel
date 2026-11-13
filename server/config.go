// Package server implements the etunnel tunnel server: WebSocket control
// sessions from SDK clients, HTTP(S) reverse proxying by hostname, plan-based
// limits (TTL / custom subdomains / bandwidth), accounts and TLS.
package server

import (
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// TLS modes.
const (
	// TLSModeHTTP serves plain HTTP (development / behind your own TLS LB).
	TLSModeHTTP = "http"
	// TLSModeStatic loads a certificate/key pair from files and hot-reloads them.
	TLSModeStatic = "static"
	// TLSModeOnDemand issues per-hostname certificates via ACME HTTP-01 on
	// first request (requires port 80 to be reachable).
	TLSModeOnDemand = "ondemand"
	// TLSModeDNS issues a wildcard certificate via ACME DNS-01 at startup and
	// renews it (requires DNS provider credentials).
	TLSModeDNS = "dns"
)

// Tier describes the limits applied to a tunnel.
type Tier struct {
	// Name is a human-readable plan name shown in welcome messages.
	Name string `json:"name"`
	// TTL is the maximum tunnel lifetime. Zero means unlimited.
	TTL time.Duration `json:"ttl"`
	// CustomDomains is how many custom subdomains an account on this tier may
	// hold simultaneously. Zero means random subdomains only.
	CustomDomains int `json:"customDomains"`
	// Bandwidth is the tunnel bandwidth cap in bits per second (applied to the
	// combined tunnel traffic). Zero means unlimited.
	Bandwidth int `json:"bandwidth"`
	// MaxTunnels is how many concurrent tunnels one account may hold. Zero
	// means unlimited. Per-IP limits cover anonymous tunnels; without this a
	// single key could hold every subdomain (issue #10).
	MaxTunnels int `json:"maxTunnels,omitempty"`
}

// TierFile is the on-disk format for custom tier definitions.
type TierFile map[string]Tier

// Config configures a Server.
type Config struct {
	// Domain is the tunnel base domain, e.g. "t.example.com". Tunnels live at
	// "<sub>.Domain".
	Domain string
	// AliasDomain serves a legacy domain alongside Domain during migrations:
	// requests for <sub>.AliasDomain route to the same tunnels. TLS for the
	// alias uses a static wildcard-<alias>.crt/key pair in the acme dir
	// (not renewed - meant for deprecation windows).
	AliasDomain string

	// Addr is the main HTTPS listen address (":443"). When TLSMode is "http"
	// this serves plain HTTP.
	Addr string
	// HTTPAddr is the plain-HTTP listen address (":80"): HTTP->HTTPS redirect
	// and ACME HTTP-01 challenges. Empty disables it.
	HTTPAddr string
	// AdminAddr is the local admin/stats listen address.
	AdminAddr string
	// AdminToken, when set, is required as X-Admin-Token for admin endpoints.
	AdminToken string

	// WSPath is the WebSocket endpoint path (default "/tunnel").
	WSPath string
	// PublicScheme overrides the URL scheme advertised in welcome messages
	// ("https" normally, "http" when TLSMode is http).
	PublicScheme string

	TLS TLSConfig

	// Tiers maps tier ID -> limits. Must contain the DefaultTier.
	Tiers map[string]*Tier
	// DefaultTier is the tier for anonymous (no API key) tunnels.
	DefaultTier string
	// DefaultPaidTier is the tier assigned to newly registered accounts.
	DefaultPaidTier string

	// AccountsFile persists registered accounts (API keys). Empty disables
	// persistence (memory only).
	AccountsFile string

	// RequireToken rejects anonymous tunnels when true.
	RequireToken bool
	// MaxTunnelsPerIP limits concurrent anonymous tunnels per source IP. 0 = unlimited.
	MaxTunnelsPerIP int
	// MaxStreamsPerTunnel limits concurrent HTTP streams per tunnel. 0 = default 256.
	MaxStreamsPerTunnel int

	HeaderWaitTimeout time.Duration // wait for client response headers (default 30s)
	StreamIdleTimeout time.Duration // per-write deadline when proxying bodies (default 10m)
	// StreamStallTimeout bounds how long one stream may block the shared
	// WebSocket reader waiting for its pump to drain. A stream that cannot
	// drain in this window is reset on its own; without it one slow external
	// consumer freezes every stream plus ping/pong handling and the session is
	// killed at PingTimeout (issue #8). Default 30s, 0 disables the bound.
	StreamStallTimeout time.Duration
	PingInterval       time.Duration // app-level ping interval (default 30s)
	PingTimeout        time.Duration // liveness threshold (default 150s)
}

// TLSConfig configures certificate management.
type TLSConfig struct {
	Mode     string // http | static | ondemand | dns
	CertFile string // static mode
	KeyFile  string // static mode
	// CacheDir stores ACME certificates and account keys.
	CacheDir string
	// ACMEEmail is the registration email for ACME CAs.
	ACMEEmail string
	// ACMECA selects the ACME directory: "prod" (default), "staging", or a URL.
	ACMECA string
	// DNSProvider selects the DNS-01 provider (dns mode): cloudflare | alidns | dnspod.
	DNSProvider string
}

// DefaultTiers returns the built-in tier set.
//
//	free    : anonymous, random subdomain, 4h TTL, 1 Mbps
//	starter : 72h TTL, 1 custom subdomain, 10 Mbps
//	pro     : 30d TTL, 5 custom subdomains, 10 Mbps
func DefaultTiers() map[string]*Tier {
	return map[string]*Tier{
		"free": {
			Name: "Free",
			TTL:  4 * time.Hour,
			// One static subdomain on the free tier (feature-gap #2, the
			// ngrok-2023 acquisition play): a registered key can keep the
			// same public URL across restarts; anonymous tunnels stay random.
			CustomDomains: 1,
			Bandwidth:     1 * 1000 * 1000, // 1 Mbps
			MaxTunnels:    2,
		},
		"starter": {
			Name:          "Starter",
			TTL:           72 * time.Hour,
			CustomDomains: 1,
			Bandwidth:     10 * 1000 * 1000, // 10 Mbps
			MaxTunnels:    5,
		},
		"pro": {
			Name:          "Pro",
			TTL:           30 * 24 * time.Hour,
			CustomDomains: 5,
			Bandwidth:     10 * 1000 * 1000,
			MaxTunnels:    20,
		},
	}
}

// normalize fills defaults and validates.
func (c *Config) normalize() error {
	if c.Domain == "" {
		return errors.New("etunnel: Domain is required")
	}
	if c.WSPath == "" {
		c.WSPath = "/tunnel"
	}
	if c.Addr == "" {
		c.Addr = ":443"
	}
	if c.AdminAddr == "" {
		c.AdminAddr = "127.0.0.1:9090"
	}
	if c.TLS.Mode == "" {
		c.TLS.Mode = TLSModeHTTP
	}
	if c.PublicScheme == "" {
		if c.TLS.Mode == TLSModeHTTP {
			c.PublicScheme = "http"
		} else {
			c.PublicScheme = "https"
		}
	}
	if c.Tiers == nil {
		c.Tiers = DefaultTiers()
	}
	if c.DefaultTier == "" {
		c.DefaultTier = "free"
	}
	if c.DefaultPaidTier == "" {
		c.DefaultPaidTier = "starter"
	}
	if _, ok := c.Tiers[c.DefaultTier]; !ok {
		return fmt.Errorf("etunnel: default tier %q not defined", c.DefaultTier)
	}
	if _, ok := c.Tiers[c.DefaultPaidTier]; !ok {
		return fmt.Errorf("etunnel: default paid tier %q not defined", c.DefaultPaidTier)
	}
	if c.HeaderWaitTimeout == 0 {
		c.HeaderWaitTimeout = 30 * time.Second
	}
	if c.StreamIdleTimeout == 0 {
		c.StreamIdleTimeout = 10 * time.Minute
	}
	if c.StreamStallTimeout == 0 {
		c.StreamStallTimeout = 30 * time.Second
	}
	if c.PingInterval == 0 {
		c.PingInterval = 30 * time.Second
	}
	if c.PingTimeout == 0 {
		c.PingTimeout = 150 * time.Second
	}
	if c.MaxStreamsPerTunnel == 0 {
		c.MaxStreamsPerTunnel = 256
	}
	switch c.TLS.Mode {
	case TLSModeHTTP, TLSModeStatic, TLSModeOnDemand, TLSModeDNS:
	default:
		return fmt.Errorf("etunnel: unknown TLS mode %q", c.TLS.Mode)
	}
	if c.TLS.Mode == TLSModeStatic && (c.TLS.CertFile == "" || c.TLS.KeyFile == "") {
		return errors.New("etunnel: static TLS mode requires CertFile and KeyFile")
	}
	if c.TLS.Mode == TLSModeOnDemand && c.TLS.CacheDir == "" {
		return errors.New("etunnel: ondemand TLS mode requires CacheDir")
	}
	if c.TLS.Mode == TLSModeDNS && c.TLS.CacheDir == "" {
		return errors.New("etunnel: dns TLS mode requires CacheDir")
	}
	// Fail fast on settings that would otherwise only break at first renewal
	// or first handshake, hours after startup (issue #16).
	if c.TLS.Mode == TLSModeDNS {
		switch c.TLS.DNSProvider {
		case "cloudflare", "alidns", "dnspod":
		case "":
			return errors.New("etunnel: dns TLS mode requires DNSProvider (cloudflare|alidns|dnspod)")
		default:
			return fmt.Errorf("etunnel: unknown DNS provider %q (cloudflare|alidns|dnspod)", c.TLS.DNSProvider)
		}
	}
	if c.TLS.Mode == TLSModeStatic {
		for _, f := range []struct{ name, path string }{
			{"CertFile", c.TLS.CertFile},
			{"KeyFile", c.TLS.KeyFile},
		} {
			if _, err := os.Stat(f.path); err != nil {
				return fmt.Errorf("etunnel: static TLS %s %q is unreadable: %w", f.name, f.path, err)
			}
		}
	}
	// Listen addresses: catch malformed host:port pairs and collisions before
	// the first bind, where the error would be opaque ("address already in use").
	seen := make(map[string]string, 3)
	for _, a := range []struct{ name, addr string }{
		{"Addr", c.Addr},
		{"HTTPAddr", c.HTTPAddr},
		{"AdminAddr", c.AdminAddr},
	} {
		if a.addr == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(a.addr); err != nil {
			return fmt.Errorf("etunnel: %s %q is not a host:port address: %w", a.name, a.addr, err)
		}
		if prev, dup := seen[a.addr]; dup {
			return fmt.Errorf("etunnel: %s and %s both listen on %q", prev, a.name, a.addr)
		}
		seen[a.addr] = a.name
	}
	return nil
}
