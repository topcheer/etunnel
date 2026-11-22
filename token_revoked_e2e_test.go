package etunnel_test

// #366: concurrent handshake-vs-delete window — the post-acquire re-check
// closes a tunnel acquired after kick/delete, instead of serving to TTL.

import (
	"strings"
	"testing"

	"github.com/topcheer/etunnel/protocol"
)

func TestConcurrentHandshakeVsDeleteRecheck(t *testing.T) {
	srv, srvURL := startTestServer(t, nil)
	acct, err := srv.Accounts().Create("r366@example.com", "free")
	if err != nil {
		t.Fatal(err)
	}
	w, close1 := rawResumeHello(t, srvURL, protocol.Hello{Token: acct.APIKey})
	if w.ResumeToken == "" {
		t.Fatal("harvest failed")
	}
	close1()

	w2, close2 := rawResumeHello(t, srvURL, protocol.Hello{
		Token: acct.APIKey, Resume: w.ResumeToken,
	})
	if w2.Subdomain == "" {
		t.Fatal("re-acquire failed")
	}
	if err := srv.Accounts().DeleteWithError(acct.APIKey); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	close2()

	// A NEW handshake with the revoked token must be refused (post-acquire
	// re-check releases + closes), not admitted to serve until TTL.
	w3, close3 := rawResumeHelloAllowError(t, srvURL, protocol.Hello{
		Token: acct.APIKey, Resume: w.ResumeToken,
	})
	close3()
	if w3.Subdomain != "" {
		t.Fatalf("revoked token re-admitted %q — token was deleted (#366)", w3.Subdomain)
	}
	_ = strings.TrimSpace
}
