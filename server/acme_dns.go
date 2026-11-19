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
	"log"
	"os"
	"path/filepath"
	"strings"
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
	// #231: load whatever is on disk FIRST. A certificate with any remaining
	// validity can serve traffic while renewal happens in the background, so a
	// transient ACME failure (DNS provider outage, CA 5xx) must not stop the
	// process from starting with 3+ weeks of perfectly good cert in hand - the
	// old code only accepted disk certs with >30d left and discarded the rest.
	// The 30-day renewal window itself is unchanged; it now also drives a
	// best-effort renewal here, with renewLoop retrying in the background.
	var disk *tls.Certificate
	if cert, err := tls.LoadX509KeyPair(w.certPath, w.keyPath); err == nil {
		disk = &cert
		w.mu.Lock()
		w.cert = disk
		w.mu.Unlock()
	}
	if disk == nil {
		// No certificate at all: obtaining one is mandatory, there is nothing
		// to fall back to.
		cert, err := w.obtain(ctx)
		if err != nil {
			return err
		}
		w.mu.Lock()
		w.cert = cert
		w.mu.Unlock()
		return nil
	}
	if time.Until(leafExpiry(disk)) <= 30*24*time.Hour {
		// Renewal window reached: try once now so a fresh cert lands promptly
		// when ACME is healthy, but a failure only logs - renewLoop retries.
		if fresh, err := w.obtain(ctx); err == nil {
			w.mu.Lock()
			w.cert = fresh
			w.mu.Unlock()
		} else {
			log.Printf("acme: renewal at startup failed (serving the cached certificate, will retry): %v", err)
		}
	}
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
			// Renewal window reached: obtain a fresh certificate. A failure
			// keeps serving the old cert and retries next tick - but it must
			// be visible, not silent: the operator needs to know the cert is
			// ageing while renewals fail (#231).
			if fresh, err := w.obtain(ctx); err == nil {
				w.mu.Lock()
				w.cert = fresh
				w.mu.Unlock()
			} else {
				days := int(time.Until(leafExpiry(cert)).Hours() / 24)
				log.Printf("acme: renewal failed, %dd left on the current certificate (will retry): %v", days, err)
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

	// SAN set: the tunnel domain + wildcard, plus the registrable root and
	// its www host so the apex domain can serve the landing page too.
	domains := []string{w.domain, "*." + w.domain}
	if i := strings.IndexByte(w.domain, '.'); i > 0 {
		root := w.domain[i+1:]
		domains = append(domains, root, "www."+root)
	}
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
	cert, err := tls.X509KeyPair(fullchain, res.PrivateKey)
	if err != nil {
		return nil, err
	}
	return &cert, nil
}
