package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"etunnel/protocol"
)

const (
	letsEncryptProdURL    = "https://acme-v02.api.letsencrypt.org/directory"
	letsEncryptStagingURL = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

type tlsConfigBundle struct {
	tlsConfig     *tls.Config
	httpChallenge http.Handler // serves /.well-known/acme-challenge on :80
}

func newTLSListener(ln net.Listener, cfg *tls.Config) net.Listener {
	return tls.NewListener(ln, cfg)
}

func (s *Server) setupTLS(ctx context.Context) (*tlsConfigBundle, error) {
	switch s.cfg.TLS.Mode {
	case TLSModeStatic:
		return s.setupStaticTLS()
	case TLSModeOnDemand:
		return s.setupOnDemandTLS()
	case TLSModeDNS:
		return s.setupDNSTLS(ctx)
	default:
		return nil, fmt.Errorf("unknown tls mode %q", s.cfg.TLS.Mode)
	}
}

// --- static -----------------------------------------------------------------

func (s *Server) setupStaticTLS() (*tlsConfigBundle, error) {
	loader := &fileCertLoader{certFile: s.cfg.TLS.CertFile, keyFile: s.cfg.TLS.KeyFile}
	if err := loader.load(); err != nil {
		return nil, err
	}
	go loader.watch()
	return &tlsConfigBundle{
		tlsConfig: &tls.Config{
			GetCertificate: loader.get,
			MinVersion:     tls.VersionTLS12,
			NextProtos:     []string{"http/1.1"},
		},
	}, nil
}

type fileCertLoader struct {
	certFile, keyFile string
	cert              atomic.Pointer[tls.Certificate]
}

func (l *fileCertLoader) load() error {
	cert, err := tls.LoadX509KeyPair(l.certFile, l.keyFile)
	if err != nil {
		return err
	}
	l.cert.Store(&cert)
	return nil
}

func (l *fileCertLoader) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	if c := l.cert.Load(); c != nil {
		return c, nil
	}
	return nil, errors.New("no certificate loaded")
}

func (l *fileCertLoader) watch() {
	stat := func() (int64, int64) {
		var cm, km int64
		if c, err := os.Stat(l.certFile); err == nil {
			cm = c.ModTime().UnixNano()
		}
		if k, err := os.Stat(l.keyFile); err == nil {
			km = k.ModTime().UnixNano()
		}
		return cm, km
	}
	lastCert, lastKey := stat()
	for range time.Tick(time.Minute) {
		c, k := stat()
		if c != lastCert || k != lastKey {
			if err := l.load(); err == nil {
				lastCert, lastKey = c, k
			}
		}
	}
}

// --- on-demand (ACME HTTP-01) -------------------------------------------------

func (s *Server) setupOnDemandTLS() (*tlsConfigBundle, error) {
	if err := os.MkdirAll(s.cfg.TLS.CacheDir, 0o700); err != nil {
		return nil, err
	}
	m := &autocert.Manager{
		Cache:      autocert.DirCache(s.cfg.TLS.CacheDir),
		Prompt:     autocert.AcceptTOS,
		Email:      s.cfg.TLS.ACMEEmail,
		HostPolicy: s.hostPolicy,
	}
	switch s.cfg.TLS.ACMECA {
	case "", "prod":
	case "staging":
		m.Client = &acme.Client{DirectoryURL: letsEncryptStagingURL}
	default:
		m.Client = &acme.Client{DirectoryURL: s.cfg.TLS.ACMECA}
	}
	return &tlsConfigBundle{
		tlsConfig: &tls.Config{
			GetCertificate: m.GetCertificate,
			MinVersion:     tls.VersionTLS12,
			NextProtos:     []string{"http/1.1"},
		},
		httpChallenge: m.HTTPHandler(nil),
	}, nil
}

// hostPolicy allows the base domain and exactly one extra label below it.
func (s *Server) hostPolicy(_ context.Context, host string) error {
	sub, isBase, isSub := s.hostLabel(host)
	if isBase || (isSub && protocol.ValidateSubdomain(sub) == nil) {
		return nil
	}
	return fmt.Errorf("host %q not allowed", host)
}

// --- wildcard via DNS-01 (lego) -----------------------------------------------

func (s *Server) setupDNSTLS(ctx context.Context) (*tlsConfigBundle, error) {
	if err := os.MkdirAll(s.cfg.TLS.CacheDir, 0o700); err != nil {
		return nil, err
	}
	return setupWildcardCert(ctx, s.cfg.TLS, s.cfg.Domain)
}
