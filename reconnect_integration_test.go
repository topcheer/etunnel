package etunnel_test

import (
	"io"
	"strings"
	"testing"
	"time"

	etunnel "github.com/topcheer/etunnel/sdk"
	"github.com/topcheer/etunnel/server"
)

// Issue #15: the CLI supervisor re-dials after a transient drop and re-requests
// the label it was given, so a paying customer's URL survives the reconnect.
// This exercises exactly what cmd/etunnel does on retry, using the real SDK and
// server (the CLI itself is a thin loop over these calls).
func TestReconnectWithSameSubdomain(t *testing.T) {
	srv, srvURL := startTestServer(t, nil)
	acct, err := srv.Accounts().Create("reconnect@example.com", "starter")
	if err != nil {
		t.Fatal(err)
	}
	srv.Accounts().Mutate("reconnect@example.com", func(a *server.Account) {
		a.Domains = []string{"stable"}
	})
	local := localMux(t)

	tun := connectTunnel(t, srvURL, local, func(o *etunnel.Options) {
		o.Token = acct.APIKey
		o.Subdomain = "stable"
	})
	firstURL, firstHost := tun.URL(), tun.Hostname()
	if !strings.HasPrefix(firstHost, "stable.") {
		t.Fatalf("custom subdomain not granted: %q", firstHost)
	}

	cli := clientThroughServer(t, srvURL)
	resp, err := cli.Get(firstURL + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("baseline request failed: %d", resp.StatusCode)
	}

	// Simulate the supervisor observing a transient drop, then re-dialing with
	// the label from the previous session (what main.go does).
	tun.Close("read closed")
	<-tun.Done()

	tun2 := connectTunnel(t, srvURL, local, func(o *etunnel.Options) {
		o.Token = acct.APIKey
		o.Subdomain = tun.Subdomain()
	})
	if tun2.Hostname() != firstHost || tun2.URL() != firstURL {
		t.Fatalf("reconnect moved the URL: %q -> %q", firstHost, tun2.Hostname())
	}

	// The reconnected tunnel must actually serve.
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := cli.Get(tun2.URL() + "/hello")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 && string(body) == "hello world" {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("reconnected tunnel did not serve: %d %q", resp.StatusCode, body)
			}
		} else if time.Now().After(deadline) {
			t.Fatalf("reconnected tunnel unreachable: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
