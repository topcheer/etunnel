// Package server implements the etunnel tunnel server: WebSocket control
// sessions from SDK clients, HTTP(S) reverse proxying by hostname, plan-based
// limits (TTL / custom subdomains / bandwidth), accounts and TLS.
package server

import (
	"errors"
	"fmt"
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
}

// TierFile is the on-disk format for custom tier definitions.
type TierFile map[string]Tier

// Config configures a Server.
type Config struct {
	// Domain is the tunnel base domain, e.g. "t.example.com". Tunnels live at
	// "<sub>.Domain".
	Domain string

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
	PingInterval      time.Duration // app-level ping interval (default 30s)
	PingTimeout       time.Duration // liveness threshold (default 150s)
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
			Name:          "Free",
			TTL:           4 * time.Hour,
			CustomDomains: 0,
			Bandwidth:     1 * 1000 * 1000, // 1 Mbps
		},
		"starter": {
			Name:          "Starter",
			TTL:           72 * time.Hour,
			CustomDomains: 1,
			Bandwidth:     10 * 1000 * 1000, // 10 Mbps
		},
		"pro": {
			Name:          "Pro",
			TTL:           30 * 24 * time.Hour,
			CustomDomains: 5,
			Bandwidth:     10 * 1000 * 1000,
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
	return nil
}
