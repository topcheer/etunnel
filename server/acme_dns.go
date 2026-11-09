package server

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/alidns"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/dnspod"
	"github.com/go-acme/lego/v4/registration"
)

// setupWildcardCert obtains (or loads) a wildcard certificate for
// [domain, *.domain] via ACME DNS-01 and renews it in the background.
// Credentials come from the DNS provider's standard environment variables.
func setupWildcardCert(ctx context.Context, cfg TLSConfig, domain string) (*tlsConfigBundle, error) {
	dir := cfg.CacheDir
	certPath := filepath.Join(dir, "wildcard-"+domain+".crt")
	keyPath := filepath.Join(dir, "wildcard-"+domain+".key")

	wc := &wildcardCert{
		certPath: certPath,
		keyPath:  keyPath,
		cfg:      cfg,
		domain:   domain,
	}

	if err := wc.loadOrObtain(ctx); err != nil {
		return nil, err
	}
	go wc.renewLoop(ctx)

	return &tlsConfigBundle{
		tlsConfig: &tls.Config{
			GetCertificate: wc.get,
			MinVersion:     tls.VersionTLS12,
			NextProtos:     []string{"http/1.1"},
		},
	}, nil
}

type wildcardCert struct {
	mu       sync.RWMutex
	cert     *tls.Certificate
	certPath string
	keyPath  string
	cfg      TLSConfig
	domain   string
}

func (w *wildcardCert) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.cert == nil {
		return nil, errors.New("etunnel: no certificate available")
	}
	return w.cert, nil
}

func (w *wildcardCert) loadOrObtain(ctx context.Context) error {
	if cert, err := tls.LoadX509KeyPair(w.certPath, w.keyPath); err == nil {
		if time.Until(leafExpiry(&cert)) > 30*24*time.Hour {
			w.mu.Lock()
			w.cert = &cert
			w.mu.Unlock()
			return nil
		}
	}
	cert, err := w.obtain(ctx)
	if err != nil {
		return err
	}
	w.mu.Lock()
	w.cert = cert
	w.mu.Unlock()
	return nil
}

func (w *wildcardCert) renewLoop(ctx context.Context) {
	ticker := time.NewTicker(12 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.mu.RLock()
			cert := w.cert
			w.mu.RUnlock()
			if cert == nil || time.Until(leafExpiry(cert)) > 30*24*time.Hour {
				continue
			}
			// Renewal window reached: obtain a fresh certificate.
			if fresh, err := w.obtain(ctx); err == nil {
				w.mu.Lock()
				w.cert = fresh
				w.mu.Unlock()
			}
		}
	}
}

func leafExpiry(cert *tls.Certificate) time.Time {
	if len(cert.Certificate) == 0 {
		return time.Now()
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return time.Now()
	}
	return leaf.NotAfter
}

type acmeUser struct {
	email string
	reg   *registration.Resource
	key   *ecdsa.PrivateKey
}

func (u *acmeUser) GetEmail() string                        { return u.email }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.reg }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

func (w *wildcardCert) obtain(ctx context.Context) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	user := &acmeUser{email: w.cfg.ACMEEmail, key: key}

	caDir := letsEncryptProdURL
	switch w.cfg.ACMECA {
	case "", "prod":
	case "staging":
		caDir = letsEncryptStagingURL
	default:
		caDir = w.cfg.ACMECA
	}

	legoCfg := lego.NewConfig(user)
	legoCfg.CADirURL = caDir
	legoCfg.Certificate.KeyType = certcrypto.EC256

	client, err := lego.NewClient(legoCfg)
	if err != nil {
		return nil, err
	}

	switch w.cfg.DNSProvider {
	case "cloudflare":
		p, err := cloudflare.NewDNSProvider()
		if err != nil {
			return nil, fmt.Errorf("cloudflare dns provider: %w", err)
		}
		client.Challenge.SetDNS01Provider(p)
	case "alidns":
		p, err := alidns.NewDNSProvider()
		if err != nil {
			return nil, fmt.Errorf("alidns dns provider: %w", err)
		}
		client.Challenge.SetDNS01Provider(p)
	case "dnspod":
		p, err := dnspod.NewDNSProvider()
		if err != nil {
			return nil, fmt.Errorf("dnspod dns provider: %w", err)
		}
		client.Challenge.SetDNS01Provider(p)
	default:
		return nil, fmt.Errorf("etunnel: dns tls mode requires -dns-provider (cloudflare|alidns|dnspod)")
	}

	reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
	if err != nil {
		return nil, fmt.Errorf("acme register: %w", err)
	}
	user.reg = reg

	domains := []string{w.domain, "*." + w.domain}
	request := certificate.ObtainRequest{Domains: domains}
	res, err := client.Certificate.Obtain(request)
	if err != nil {
		return nil, fmt.Errorf("acme obtain %v: %w", domains, err)
	}

	// Fullchain: res.Certificate is the bare leaf; IssuerCertificate is the
	// intermediate. Clients with strict CA stores (node/undici, openssl,
	// JVM) reject leaf-only servers - macOS curl masks it via AIA fetching.
	fullchain := append(append([]byte{}, res.Certificate...), res.IssuerCertificate...)
	if err := os.WriteFile(w.certPath, fullchain, 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(w.keyPath, res.PrivateKey, 0o600); err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(res.Certificate, res.PrivateKey)
	if err != nil {
		return nil, err
	}
	return &cert, nil
}
