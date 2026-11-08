package etunnel_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	etunnel "github.com/topcheer/etunnel/sdk"
	"github.com/topcheer/etunnel/server"
)

const testDomain = "t.test"

func startTestServer(t *testing.T, mut func(*server.Config)) (*server.Server, string) {
	t.Helper()
	cfg := server.Config{
		Domain:       testDomain,
		WSPath:       "/tunnel",
		TLS:          server.TLSConfig{Mode: server.TLSModeHTTP},
		AccountsFile: filepath.Join(t.TempDir(), "accounts.json"),
		AdminAddr:    "",
	}
	cfg.Tiers = server.DefaultTiers()
	if mut != nil {
		mut(&cfg)
	}
	srv, err := server.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts.URL
}

func sdkURL(serverURL string) string {
	return "ws" + strings.TrimPrefix(serverURL, "http") + "/tunnel"
}

func clientThroughServer(t *testing.T, serverURL string) *http.Client {
	t.Helper()
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, u.Host)
	}
	tr := &http.Transport{ForceAttemptHTTP2: false, DialContext: dial}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr}
}

func connectTunnel(t *testing.T, serverURL string, localURL string, mut func(*etunnel.Options)) *etunnel.Tunnel {
	t.Helper()
	local := strings.TrimPrefix(localURL, "http://")
	opts := etunnel.Options{Server: sdkURL(serverURL), LocalAddr: local}
	if mut != nil {
		mut(&opts)
	}
	tun, err := etunnel.Connect(context.Background(), opts)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { tun.Close("test done") })
	return tun
}

func newLocalService(t *testing.T, h http.Handler) string {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts.URL
}

func localMux(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Forwarded-For") == "" {
			t.Error("missing X-Forwarded-For at local service")
		}
		w.Header().Set("X-Local", "yes")
		io.WriteString(w, "hello world")
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(b)
	})
	return newLocalService(t, mux)
}

func TestBasicHTTPTunnel(t *testing.T) {
	_, srvURL := startTestServer(t, nil)
	local := localMux(t)
	tun := connectTunnel(t, srvURL, local, nil)
	if tun.URL() == "" || !strings.HasSuffix(tun.Hostname(), "."+testDomain) {
		t.Fatalf("bad welcome: url=%q host=%q", tun.URL(), tun.Hostname())
	}
	if !tun.ExpiresAt().After(time.Now()) {
		t.Fatal("free tunnel should have 4h expiry")
	}

	cli := clientThroughServer(t, srvURL)

	resp, err := cli.Get(tun.URL() + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "hello world" {
		t.Fatalf("GET failed: %d %q", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Local") != "yes" {
		t.Fatal("response header not forwarded")
	}

	big := bytes.Repeat([]byte("etunnel!"), 64*1024)
	resp, err = cli.Post(tun.URL()+"/echo", "application/octet-stream", bytes.NewReader(big))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Equal(body, big) {
		t.Fatalf("POST echo failed: %d len=%d", resp.StatusCode, len(body))
	}
}

func TestStreamingResponse(t *testing.T) {
	_, srvURL := startTestServer(t, nil)
	streamer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f := w.(http.Flusher)
		for i := 0; i < 5; i++ {
			io.WriteString(w, "chunk-")
			f.Flush()
			time.Sleep(30 * time.Millisecond)
		}
	})
	local := newLocalService(t, streamer)
	tun := connectTunnel(t, srvURL, local, nil)
	cli := clientThroughServer(t, srvURL)

	resp, err := cli.Get(tun.URL() + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "chunk-chunk-chunk-chunk-chunk-" {
		t.Fatalf("stream body mismatch: %q", got)
	}
}

func TestUnknownSubdomain404(t *testing.T) {
	_, srvURL := startTestServer(t, nil)
	cli := clientThroughServer(t, srvURL)
	resp, err := cli.Get("http://nope-does-not-exist." + testDomain + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestLandingPage(t *testing.T) {
	_, srvURL := startTestServer(t, nil)
	cli := clientThroughServer(t, srvURL)
	resp, err := cli.Get("http://" + testDomain + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(b), "etunnel") {
		t.Fatalf("landing page missing: %d %q", resp.StatusCode, b)
	}
}

func TestAnonymousCannotUseCustomSubdomain(t *testing.T) {
	_, srvURL := startTestServer(t, nil)
	local := newLocalService(t, http.NotFoundHandler())
	_, err := etunnel.Connect(context.Background(), etunnel.Options{
		Server:    sdkURL(srvURL),
		LocalAddr: strings.TrimPrefix(local, "http://"),
		Subdomain: "myname",
	})
	apiErr, ok := err.(*etunnel.APIError)
	if !ok {
		t.Fatalf("expected APIError, got %v", err)
	}
	if apiErr.Code != "subdomain_plan" {
		t.Fatalf("expected subdomain_plan, got %s", apiErr.Code)
	}
}

func TestPaidCustomSubdomains(t *testing.T) {
	srv, srvURL := startTestServer(t, nil)
	acct, err := srv.Accounts().Create("dev@example.com", "starter")
	if err != nil {
		t.Fatal(err)
	}
	// Ownership gate: custom names must be added to the account first (portal /me).
	srv.Accounts().Mutate("dev@example.com", func(a *server.Account) { a.Domains = []string{"mydev"} })
	local := newLocalService(t, http.NotFoundHandler())

	tun := connectTunnel(t, srvURL, local, func(o *etunnel.Options) {
		o.Token = acct.APIKey
		o.Subdomain = "mydev"
	})
	if !strings.HasPrefix(tun.Hostname(), "mydev.") {
		t.Fatalf("custom hostname not assigned: %q", tun.Hostname())
	}
	if tun.ExpiresAt().Before(time.Now().Add(71 * time.Hour)) {
		t.Fatal("starter tier should get 72h ttl")
	}

	_, err = etunnel.Connect(context.Background(), etunnel.Options{
		Server:    sdkURL(srvURL),
		LocalAddr: strings.TrimPrefix(local, "http://"),
		Token:     acct.APIKey,
		Subdomain: "other",
	})
	apiErr, ok := err.(*etunnel.APIError)
	if !ok || apiErr.Code != "limit_reached" {
		t.Fatalf("expected limit_reached, got %v", err)
	}

	tun2 := connectTunnel(t, srvURL, local, func(o *etunnel.Options) {
		o.Token = acct.APIKey
	})
	if tun2.Hostname() == tun.Hostname() {
		t.Fatal("second tunnel got the same hostname")
	}
}

func TestBadTokenRejected(t *testing.T) {
	_, srvURL := startTestServer(t, nil)
	local := newLocalService(t, http.NotFoundHandler())
	_, err := etunnel.Connect(context.Background(), etunnel.Options{
		Server:    sdkURL(srvURL),
		LocalAddr: strings.TrimPrefix(local, "http://"),
		Token:     "et_bogus",
	})
	apiErr, ok := err.(*etunnel.APIError)
	if !ok || apiErr.Code != "unauthorized" {
		t.Fatalf("expected unauthorized, got %v", err)
	}
}

func TestTTLExpiry(t *testing.T) {
	_, srvURL := startTestServer(t, func(c *server.Config) {
		c.Tiers["micro"] = &server.Tier{Name: "Micro", TTL: 400 * time.Millisecond}
		c.DefaultTier = "micro"
	})
	local := newLocalService(t, http.NotFoundHandler())
	tun := connectTunnel(t, srvURL, local, nil)

	select {
	case <-tun.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("tunnel did not expire")
	}
	if tun.CloseReason() != "ttl_expired" {
		t.Fatalf("expected ttl_expired, got %q", tun.CloseReason())
	}
}

func TestBandwidthLimit(t *testing.T) {
	_, srvURL := startTestServer(t, func(c *server.Config) {
		c.Tiers["slow"] = &server.Tier{Name: "Slow", Bandwidth: 512_000}
		c.DefaultTier = "slow"
	})
	payload := bytes.Repeat([]byte("x"), 256*1024)
	big := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	})
	local := newLocalService(t, big)
	tun := connectTunnel(t, srvURL, local, nil)
	cli := clientThroughServer(t, srvURL)

	start := time.Now()
	resp, err := cli.Get(tun.URL() + "/big")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(payload) {
		t.Fatalf("body truncated: %d", len(got))
	}
	if elapsed < 1200*time.Millisecond {
		t.Fatalf("bandwidth limit not enforced: elapsed %v", elapsed)
	}
	t.Logf("256KiB took %v under a 512kbps cap", elapsed)
}

func wsEcho(t *testing.T) string {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close(websocket.StatusNormalClosure, "")
		for {
			_, rd, err := ws.Reader(r.Context())
			if err != nil {
				return
			}
			msg, _ := io.ReadAll(rd)
			if err := ws.Write(r.Context(), websocket.MessageText, msg); err != nil {
				return
			}
		}
	})
	return newLocalService(t, h)
}

func TestWebSocketPassthrough(t *testing.T) {
	_, srvURL := startTestServer(t, nil)
	local := wsEcho(t)
	tun := connectTunnel(t, srvURL, local, nil)

	u, _ := url.Parse(srvURL)
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, u.Host)
	}
	wsClient := &http.Client{Transport: &http.Transport{ForceAttemptHTTP2: false, DialContext: dial}}

	wsURL := "ws" + strings.TrimPrefix(tun.URL(), "http") + "/echo"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: wsClient})
	if err != nil {
		t.Fatalf("ws dial through tunnel: %v", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")

	for i := 0; i < 3; i++ {
		msg := []byte("ping-through-tunnel")
		if err := ws.Write(ctx, websocket.MessageText, msg); err != nil {
			t.Fatalf("write: %v", err)
		}
		_, rd, err := ws.Reader(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		got, _ := io.ReadAll(rd)
		if !bytes.Equal(got, msg) {
			t.Fatalf("echo mismatch: %q", got)
		}
	}
}

func TestConcurrentRequests(t *testing.T) {
	_, srvURL := startTestServer(t, nil)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		io.WriteString(w, r.URL.Query().Get("id"))
	})
	local := newLocalService(t, h)
	tun := connectTunnel(t, srvURL, local, nil)
	cli := clientThroughServer(t, srvURL)

	var wg sync.WaitGroup
	var mu sync.Mutex
	failed := 0
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := cli.Get(tun.URL() + "/?id=" + string(rune('a'+i%26)))
			if err != nil {
				mu.Lock()
				failed++
				mu.Unlock()
				return
			}
			io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 {
				mu.Lock()
				failed++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if failed > 0 {
		t.Fatalf("%d/24 concurrent requests failed", failed)
	}
}
