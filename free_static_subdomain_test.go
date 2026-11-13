package etunnel_test

// Free-tier static subdomain (feature-gap #2): a registered free key owns
// ONE static subdomain (claimed at /me like paid names, so the URL is
// stable across restarts); a second name is refused by the quota gate;
// anonymous tunnels keep the plan error.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	etunnel "github.com/topcheer/etunnel/sdk"
	"github.com/topcheer/etunnel/server"
)

func TestFreeStaticSubdomain(t *testing.T) {
	srv, srvURL := startTestServer(t, nil)
	client := clientThroughServer(t, srvURL)
	local := newLocalService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	localAddr := strings.TrimPrefix(local, "http://")

	acct, err := srv.Accounts().Create("free-dev@example.com", "free")
	if err != nil {
		t.Fatal(err)
	}
	// Claim the name on the account first (portal /me ownership gate),
	// which the free quota (CustomDomains=1) now permits.
	if !srv.Accounts().TryClaimDomain("free-dev@example.com", "mydev", func(a *server.Account) bool {
		return len(a.Domains) < 1 // free quota: one static subdomain
	}) {
		t.Fatal("free account should claim exactly one static subdomain")
	}
	// A second name exceeds the quota.
	if srv.Accounts().TryClaimDomain("free-dev@example.com", "second", func(a *server.Account) bool {
		return len(a.Domains) < 1
	}) {
		t.Fatal("second claim should hit the free quota")
	}

	// Serve through the static subdomain.
	tun, err := etunnel.Connect(context.Background(), etunnel.Options{
		Server:    sdkURL(srvURL),
		LocalAddr: localAddr,
		Token:     acct.APIKey,
		Subdomain: "mydev",
	})
	if err != nil {
		t.Fatalf("free static subdomain connect: %v", err)
	}
	defer tun.Close("done")
	if !strings.HasPrefix(tun.URL(), "http://mydev.") {
		t.Fatalf("URL = %q, want mydev subdomain", tun.URL())
	}
	resp, err := client.Get(tun.URL())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("static subdomain serve = %d", resp.StatusCode)
	}

	// Anonymous (no key) still gets the plan error.
	_, err = etunnel.Connect(context.Background(), etunnel.Options{
		Server:    sdkURL(srvURL),
		LocalAddr: localAddr,
		Subdomain: "anonname",
	})
	if apiErr, ok := err.(*etunnel.APIError); !ok || apiErr.Code != "subdomain_plan" {
		t.Fatalf("anonymous claim err = %v, want subdomain_plan", err)
	}
}
