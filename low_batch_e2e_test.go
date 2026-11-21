package etunnel_test

// #304 A-4: the TTL timer is assembled from the expiry actually granted
// (sess.expires), not from the tier's current TTL. A session resumed with a
// hard expiry carried in its token must be reaped when that expiry passes,
// even after the account landed on a TTL=0 tier.

import (
	"net/http"
	"testing"
	"time"

	"github.com/topcheer/etunnel/protocol"
	"github.com/topcheer/etunnel/server"
)

func TestResumeOnUntimedTierStillReaped(t *testing.T) {
	srv, srvURL := startTestServer(t, func(c *server.Config) {
		c.Tiers["timed"] = &server.Tier{
			Name: "Timed", TTL: 2 * time.Second,
			CustomDomains: 1, MaxTunnels: 5,
		}
		c.Tiers["untimed"] = &server.Tier{
			Name: "Untimed", TTL: 0,
			CustomDomains: 1, MaxTunnels: 5,
		}
	})

	acct, err := srv.Accounts().Create("a4@example.com", "timed")
	if err != nil {
		t.Fatal(err)
	}
	if !srv.Accounts().TryClaimDomain("a4@example.com", "a4low", func(a *server.Account) bool {
		return len(a.Domains) < 1
	}) {
		t.Fatal("claim a4low")
	}

	// Take the subdomain on the timed tier; the welcome carries the token
	// whose signed session expiry is now+300ms.
	w1, close1 := rawResumeHello(t, srvURL, protocol.Hello{
		Token: acct.APIKey, Subdomain: "a4low",
	})
	if w1.Subdomain != "a4low" || w1.ResumeToken == "" || w1.ExpiresAt == 0 {
		t.Fatalf("initial connect: sub=%q expires=%d", w1.Subdomain, w1.ExpiresAt)
	}
	original := w1.ExpiresAt
	close1()

	// Lapse onto the untimed tier (TTL=0) before resuming.
	if !srv.Accounts().Mutate("a4@example.com", func(a *server.Account) { a.Tier = "untimed" }) {
		t.Fatal("downgrade account")
	}

	// The resumed session keeps the signed expiry (A-4 precondition) and -
	// the actual A-4 assertion - is still reaped when it passes, despite the
	// tier now having no TTL.
	w2, close2 := rawResumeHello(t, srvURL, protocol.Hello{
		Token: acct.APIKey, Resume: w1.ResumeToken,
	})
	if w2.Subdomain != "a4low" {
		t.Fatalf("resume lost the subdomain: %q", w2.Subdomain)
	}
	if w2.ExpiresAt != original {
		t.Fatalf("resumed expiry %d != original %d", w2.ExpiresAt, original)
	}
	close2()

	cli := clientThroughServer(t, srvURL)
	deadline := time.Now().Add(5 * time.Second)
	reaped := false
	for time.Now().Before(deadline) {
		resp, err := cli.Get(w2.URL)
		if err == nil {
			if resp.StatusCode == http.StatusNotFound {
				reaped = true
				break
			}
			resp.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !reaped {
		t.Fatal("resumed session with carried expiry survived its TTL on an untimed tier (#304 A-4)")
	}
}
