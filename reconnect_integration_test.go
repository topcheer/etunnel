package etunnel_test

// End-to-end proof for reconnect + subdomain retention: the client survives a
// server restart, reconnects on its own, and gets the SAME public hostname
// back (via the signed resume token), so links already shared keep working.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	etunnel "github.com/topcheer/etunnel/sdk"
	"github.com/topcheer/etunnel/server"
)

// restartableServer runs an etunneld handler on a fixed port so the test can
// stop it and bring a fresh instance up on the same address.
type restartableServer struct {
	addr string
	srv  *http.Server
	ln   *trackingListener
}

// trackingListener remembers accepted conns so stop() can hard-close them the
// way a process exit would. http.Server.Shutdown deliberately leaves hijacked
// connections (which a WebSocket is) alone, so it cannot simulate a restart.
type trackingListener struct {
	net.Listener
	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

func newTrackingListener(ln net.Listener) *trackingListener {
	return &trackingListener{Listener: ln, conns: map[net.Conn]struct{}{}}
}

func (l *trackingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return c, err
	}
	l.mu.Lock()
	l.conns[c] = struct{}{}
	l.mu.Unlock()
	return c, nil
}

func (l *trackingListener) closeAll() {
	l.mu.Lock()
	for c := range l.conns {
		_ = c.Close()
	}
	l.conns = map[net.Conn]struct{}{}
	l.mu.Unlock()
	_ = l.Listener.Close()
}

func startOn(t *testing.T, addr string) *restartableServer {
	t.Helper()
	cfg := server.Config{
		Domain:       "t.test",
		Addr:         "127.0.0.1:1",
		TLS:          server.TLSConfig{Mode: server.TLSModeHTTP},
		AccountsFile: filepath.Join(t.TempDir(), "a.json"),
		Tiers:        server.DefaultTiers(),
	}
	s, err := server.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	tl := newTrackingListener(ln)
	hs := &http.Server{Handler: s.Handler()}
	go hs.Serve(tl)
	return &restartableServer{addr: ln.Addr().String(), srv: hs, ln: tl}
}

// stop simulates a process restart: every socket is closed immediately, so
// the client sees the disconnect the same way it would in production.
func (r *restartableServer) stop() {
	r.ln.closeAll()
	_ = r.srv.Close()
}

func TestReconnectKeepsSubdomainAcrossServerRestart(t *testing.T) {
	// local service the tunnel points at
	localLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer localLn.Close()
	go func() {
		for {
			c, err := localLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 512)
				_, _ = c.Read(buf)
				body := "hello-from-local"
				fmt.Fprintf(c, "HTTP/1.1 200 OK\r\ncontent-length: %d\r\n\r\n%s", len(body), body)
			}(c)
		}
	}()

	first := startOn(t, "127.0.0.1:0")
	serverURL := "ws://" + first.addr + "/tunnel"

	var reconnects atomic.Int32
	var lastURL atomic.Value
	rt, err := etunnel.ConnectReconnecting(context.Background(), etunnel.Options{
		Server:              serverURL,
		LocalAddr:           localLn.Addr().String(),
		ReconnectMinBackoff: 100 * time.Millisecond,
		ReconnectMaxBackoff: time.Second,
		OnReconnect: func(attempt int, url string) {
			reconnects.Add(1)
			lastURL.Store(url)
			t.Logf("OnReconnect #%d -> %s", attempt, url)
		},
		OnLog: func(format string, args ...any) {
			t.Logf("sdk: "+format, args...)
		},
	})
	if err != nil {
		t.Fatalf("initial connect: %v", err)
	}
	defer rt.Close("test done")

	original := rt.URL()
	sub := rt.Subdomain()
	if original == "" || sub == "" {
		t.Fatal("no url/subdomain")
	}
	t.Logf("initial tunnel: %s", original)

	// the tunnel serves traffic before the restart
	assertServes(t, first.addr, original)

	// restart the server: same port, brand-new process state (empty registry)
	first.stop()
	time.Sleep(150 * time.Millisecond)
	second := startOn(t, first.addr)
	defer second.stop()

	// wait for the supervisor to notice and reconnect
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if reconnects.Load() > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if reconnects.Load() == 0 {
		t.Fatal("client never reconnected after the server restart")
	}
	if got := lastURL.Load(); got == nil || got.(string) != original {
		t.Fatalf("URL after reconnect = %v, want the original %s (subdomain must be retained)", got, original)
	}
	if rt.Subdomain() != sub {
		t.Fatalf("subdomain changed: %s -> %s", sub, rt.Subdomain())
	}
	// the reclaimed tunnel actually serves again on the same hostname
	assertServes(t, second.addr, original)
	t.Logf("reconnected %d time(s), hostname preserved: %s", reconnects.Load(), original)
}

func assertServes(t *testing.T, serverAddr, publicURL string) {
	t.Helper()
	host := strings.TrimPrefix(publicURL, "http://")
	req, _ := http.NewRequest(http.MethodGet, "http://"+serverAddr+"/", nil)
	req.Host = host // route by Host header, no DNS needed
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s via %s: %v", host, serverAddr, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(b) != "hello-from-local" {
		t.Fatalf("GET %s -> %d %q", host, resp.StatusCode, b)
	}
}
