// Login: the CLI loopback authorization flow (see server/auth_cli.go).
//
// The SDK listens on a random 127.0.0.1 port, asks the server for an
// authorization URL, opens the user's browser there, waits for the browser
// to land back on the loopback port with a one-time code, and exchanges it
// for the account's API token and custom-domain list. The caller persists
// the returned Credentials and passes APIToken as Options.Token on future
// Connect calls.
package etunnel

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// Credentials is what Login returns and what applications persist.
type Credentials struct {
	// APIToken authenticates tunnel creation (Options.Token).
	APIToken string `json:"api_token"`
	// Subdomains are the account's custom domains (empty for free tier).
	Subdomains []string `json:"subdomains"`
	// Plan is the account tier ("free", ...).
	Plan string `json:"plan"`
	// ExpiresAt is 0: API tokens do not lapse on their own.
	ExpiresAt int64 `json:"expires_at"`
}

// LoginTimeout bounds the whole flow: browser open, user login (if needed)
// and approval. The server's own code lives 5 minutes; 10 minutes here
// covers a login detour.
const LoginTimeout = 10 * time.Minute

// Login runs the loopback authorization flow against server (e.g.
// "https://t.etun.uk"). openBrowser may be nil; headless environments get
// the URL printed to stderr instead.
func Login(ctx context.Context, server string, openBrowser func(string) error) (*Credentials, error) {
	ctx, cancel := context.WithTimeout(ctx, LoginTimeout)
	defer cancel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("etunnel: listen loopback: %w", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	// 1. Ask the server for the authorization URL + device code.
	start := &struct {
		AuthURL    string `json:"auth_url"`
		DeviceCode string `json:"device_code"`
	}{}
	if err := loginPost(ctx, server, "/auth/cli/start", map[string]string{"redirect_uri": redirectURI}, start); err != nil {
		return nil, err
	}
	if start.DeviceCode == "" || start.AuthURL == "" {
		return nil, errors.New("etunnel: server returned an empty authorization start")
	}

	// 2. Serve exactly one callback, then hang up.
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		code := r.URL.Query().Get("code")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if code == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, "<p>missing code - retry the CLI login</p>")
			select {
			case errCh <- errors.New("etunnel: callback without code"):
			default:
			}
			return
		}
		_, _ = io.WriteString(w, "<p>Authorized. You can close this tab and return to the terminal.</p>")
		select {
		case codeCh <- code:
		default:
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	// 3. Open the browser (or tell the user where to go).
	if openBrowser == nil {
		openBrowser = openBrowserOS
	}
	if err := openBrowser(start.AuthURL); err != nil {
		// Not fatal: print the URL so a human can ferry it to a browser.
		fmt.Fprintf(stdErr, "etunnel: open the browser yourself: %s\n", start.AuthURL)
	}

	// 4. Wait for the loopback callback with the one-time code.
	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		return nil, fmt.Errorf("etunnel: login timed out or cancelled: %w", ctx.Err())
	}

	// 5. Exchange code + device code for credentials.
	creds := &Credentials{}
	if err := loginPost(ctx, server, "/auth/cli/exchange", map[string]string{
		"device_code": start.DeviceCode,
		"code":        code,
	}, creds); err != nil {
		return nil, err
	}
	if creds.APIToken == "" {
		return nil, errors.New("etunnel: server returned no api_token")
	}
	return creds, nil
}

// loginPost is a small JSON POST helper (the login flow runs before any
// tunnel exists, so it cannot reuse the websocket machinery).
func loginPost(ctx context.Context, server, path string, body any, out any) error {
	u, err := url.JoinPath(server, path)
	if err != nil {
		return fmt.Errorf("etunnel: bad server url: %w", err)
	}
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		// Self-signed dev servers: opt in per call by using an https:// URL
		// with a host of localhost/127.0.0.1 - same policy as the tunnel
		// dialer. Public servers verify normally.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: isLocalURL(u)},
	}}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("etunnel: %s: %w", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("etunnel: %s: %s: %s", path, resp.Status, string(b))
	}
	return json.Unmarshal(b, out)
}

// openBrowser tries the platform opener and reports whether it failed
// (headless boxes, missing xdg-open): the caller prints the URL instead.
func openBrowserOS(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start()
}

// isLocalURL reports whether u points at this machine (dev servers with
// self-signed certificates); such URLs skip TLS verification.
func isLocalURL(u string) bool {
	pu, err := url.Parse(u)
	if err != nil {
		return false
	}
	h := pu.Hostname()
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

// stdErr is a seam for tests.
var stdErr io.Writer = os.Stderr
