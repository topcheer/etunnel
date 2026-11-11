package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #16: configurations that cannot work must fail at startup with an
// actionable message, not at the first handshake or certificate renewal.

func baseCfg() Config {
	return Config{Domain: "t.example.com"}
}

func TestNormalizeRejectsDNSPModeWithoutProvider(t *testing.T) {
	c := baseCfg()
	c.TLS = TLSConfig{Mode: TLSModeDNS, CacheDir: t.TempDir()}
	err := c.normalize()
	if err == nil || !strings.Contains(err.Error(), "DNSProvider") {
		t.Fatalf("want a DNSProvider error, got %v", err)
	}

	c.TLS.DNSProvider = "bogus"
	err = c.normalize()
	if err == nil || !strings.Contains(err.Error(), "unknown DNS provider") {
		t.Fatalf("want an unknown-provider error, got %v", err)
	}

	c.TLS.DNSProvider = "cloudflare"
	if err := c.normalize(); err != nil {
		t.Fatalf("valid dns config rejected: %v", err)
	}
}

func TestNormalizeChecksStaticCertFiles(t *testing.T) {
	dir := t.TempDir()
	c := baseCfg()
	c.TLS = TLSConfig{Mode: TLSModeStatic, CertFile: filepath.Join(dir, "missing.crt"), KeyFile: filepath.Join(dir, "missing.key")}
	err := c.normalize()
	if err == nil || !strings.Contains(err.Error(), "CertFile") {
		t.Fatalf("want a CertFile error, got %v", err)
	}

	cert := filepath.Join(dir, "fullchain.pem")
	key := filepath.Join(dir, "privkey.pem")
	for _, f := range []string{cert, key} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c.TLS.CertFile, c.TLS.KeyFile = cert, key
	if err := c.normalize(); err != nil {
		t.Fatalf("valid static config rejected: %v", err)
	}
}

func TestNormalizeChecksListenAddresses(t *testing.T) {
	c := baseCfg()
	c.Addr = "443" // missing colon
	if err := c.normalize(); err == nil || !strings.Contains(err.Error(), "host:port") {
		t.Fatalf("want a host:port error, got %v", err)
	}

	// AdminAddr defaults to 127.0.0.1:9090; reusing it for the public listener
	// must be rejected instead of failing at bind time.
	c = baseCfg()
	c.Addr = "127.0.0.1:9090"
	if err := c.normalize(); err == nil || !strings.Contains(err.Error(), "both listen") {
		t.Fatalf("want a collision error, got %v", err)
	}

	c = baseCfg()
	if err := c.normalize(); err != nil {
		t.Fatalf("defaults rejected: %v", err)
	}
}
