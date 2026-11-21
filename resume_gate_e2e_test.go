package etunnel_test

// #292 regressions: the resume path no longer short-circuits identity and
// plan gating. Each test drives raw hello/welcome handshakes against a real
// server so the exact wire behavior is pinned.

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/topcheer/etunnel/protocol"
	"github.com/topcheer/etunnel/server"
)

// rawResumeHello dials the tunnel WS, sends hello, and returns the welcome.
// The connection is left open for the caller (close via the returned func).
func rawResumeHello(t *testing.T, srvURL string, hello protocol.Hello) (protocol.Welcome, func()) {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srvURL, "http") + "/tunnel"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		cancel()
		t.Fatalf("dial tunnel: %v", err)
	}
	hello.Type = "hello"
	hello.Proto = protocol.ProtoVersion
	raw, _ := json.Marshal(hello)
	if err := ws.Write(ctx, websocket.MessageText, raw); err != nil {
		ws.Close(websocket.StatusNormalClosure, "")
		cancel()
		t.Fatalf("hello: %v", err)
	}
	_, rd, err := ws.Reader(ctx)
	if err != nil {
		ws.Close(websocket.StatusNormalClosure, "")
		cancel()
		t.Fatalf("welcome read: %v", err)
	}
	msg, err := io.ReadAll(rd)
	if err != nil {
		ws.Close(websocket.StatusNormalClosure, "")
		cancel()
		t.Fatalf("welcome: %v", err)
	}
	var w protocol.Welcome
	if err := json.Unmarshal(msg, &w); err != nil || w.Type != "welcome" {
		ws.Close(websocket.StatusNormalClosure, "")
		cancel()
		t.Fatalf("expected welcome, got %q", strings.TrimSpace(string(msg)))
	}
	return w, func() {
		ws.Close(websocket.StatusNormalClosure, "")
		cancel()
	}
}

// TestResumeCrossAccountRejected pins #292 F1: a resume token is bound to
// the identity that received it. Account B (and an anonymous connection)
// presenting account A's token must fall back to a fresh subdomain, while A
// itself still reclaims the name.
func TestResumeCrossAccountRejected(t *testing.T) {
	srv, srvURL := startTestServer(t, nil)

	acctA, err := srv.Accounts().Create("ra-a@example.com", "free")
	if err != nil {
		t.Fatal(err)
	}
	acctB, err := srv.Accounts().Create("ra-b@example.com", "free")
	if err != nil {
		t.Fatal(err)
	}
	if !srv.Accounts().TryClaimDomain("ra-a@example.com", "crossa", func(a *server.Account) bool {
		return len(a.Domains) < 1
	}) {
		t.Fatal("claim crossa")
	}

	// A takes its static subdomain and receives the resume token.
	w1, close1 := rawResumeHello(t, srvURL, protocol.Hello{
		Token: acctA.APIKey, Subdomain: "crossa",
	})
	tok := w1.ResumeToken
	if tok == "" || w1.Subdomain != "crossa" {
		t.Fatalf("initial connect: sub=%q tok=%q", w1.Subdomain, tok)
	}
	close1()

	// Account B replays A's token: identity binding must reject it.
	w2, close2 := rawResumeHello(t, srvURL, protocol.Hello{
		Token: acctB.APIKey, Resume: tok,
	})
	if w2.Subdomain == "crossa" {
		t.Fatalf("cross-account resume took over %q (#292 F1)", w2.Subdomain)
	}
	close2()

	// An anonymous connection replaying A's token must be rejected too.
	w3, close3 := rawResumeHello(t, srvURL, protocol.Hello{Resume: tok})
	if w3.Subdomain == "crossa" {
		t.Fatalf("anonymous replay took over %q (#292 F1)", w3.Subdomain)
	}
	close3()

	// Positive control: A itself still reclaims the name.
	w4, close4 := rawResumeHello(t, srvURL, protocol.Hello{
		Token: acctA.APIKey, Resume: tok,
	})
	if w4.Subdomain != "crossa" {
		t.Fatalf("legitimate same-account resume lost %q: got %q", "crossa", w4.Subdomain)
	}
	close4()
}

// TestResumeOverQuotaRejected pins #292 F2: resuming a requested subdomain
// re-passes the same custom-subdomain quota the request path applies. A pro
// account that legitimately held two names (harvesting a token for each) and
// then lapses to free (quota 1) can bring back the first tunnel but not the
// second - concurrent double-resume cannot exceed the quota.
func TestResumeOverQuotaRejected(t *testing.T) {
	srv, srvURL := startTestServer(t, nil)

	acct, err := srv.Accounts().Create("quota@example.com", "pro") // CustomDomains=5
	if err != nil {
		t.Fatal(err)
	}
	if !srv.Accounts().TryClaimDomain("quota@example.com", "gatea", func(a *server.Account) bool {
		return len(a.Domains) < 2
	}) {
		t.Fatal("claim gatea")
	}
	if !srv.Accounts().TryClaimDomain("quota@example.com", "gateb", func(a *server.Account) bool {
		return len(a.Domains) < 2
	}) {
		t.Fatal("claim gateb")
	}
	key := acct.APIKey

	// Harvest a custom resume token per name while the plan allows both.
	w1, c1 := rawResumeHello(t, srvURL, protocol.Hello{Token: key, Subdomain: "gatea"})
	if w1.Subdomain != "gatea" || w1.ResumeToken == "" {
		t.Fatalf("connect gatea: sub=%q", w1.Subdomain)
	}
	c1()
	w2, c2 := rawResumeHello(t, srvURL, protocol.Hello{Token: key, Subdomain: "gateb"})
	if w2.Subdomain != "gateb" || w2.ResumeToken == "" {
		t.Fatalf("connect gateb: sub=%q", w2.Subdomain)
	}
	c2()

	// Subscription lapse: the reaper downgrades the account to free
	// (CustomDomains=1) without touching the owned names.
	if !srv.Accounts().Mutate("quota@example.com", func(a *server.Account) { a.Tier = "free" }) {
		t.Fatal("downgrade account")
	}

	// First resume: quota free slot -> allowed.
	r1, rc1 := rawResumeHello(t, srvURL, protocol.Hello{Token: key, Resume: w1.ResumeToken})
	if r1.Subdomain != "gatea" {
		t.Fatalf("first resume should reclaim gatea, got %q", r1.Subdomain)
	}
	// Second concurrent resume: free quota is 1 and gatea is live -> rejected.
	r2, rc2 := rawResumeHello(t, srvURL, protocol.Hello{Token: key, Resume: w2.ResumeToken})
	if r2.Subdomain == "gateb" {
		t.Fatalf("over-quota double resume took gateb (#292 F2)")
	}
	rc1()
	rc2()
}

// TestResumeDoesNotExtendTTL pins #292 F3: a resumed session keeps the hard
// expiry its original session was granted; resume must not reset the tier
// TTL (a cron of rolling resumes cannot keep a free tunnel alive forever).
func TestResumeDoesNotExtendTTL(t *testing.T) {
	srv, srvURL := startTestServer(t, nil)

	acct, err := srv.Accounts().Create("ttl@example.com", "free") // TTL 4h
	if err != nil {
		t.Fatal(err)
	}
	if !srv.Accounts().TryClaimDomain("ttl@example.com", "ttla", func(a *server.Account) bool {
		return len(a.Domains) < 1
	}) {
		t.Fatal("claim ttla")
	}

	w1, c1 := rawResumeHello(t, srvURL, protocol.Hello{
		Token: acct.APIKey, Subdomain: "ttla",
	})
	if w1.ExpiresAt == 0 || w1.ResumeToken == "" {
		t.Fatalf("initial welcome missing expiry/token: %+v", w1)
	}
	c1()

	w2, c2 := rawResumeHello(t, srvURL, protocol.Hello{
		Token: acct.APIKey, Resume: w1.ResumeToken,
	})
	if w2.Subdomain != "ttla" {
		t.Fatalf("resume lost the subdomain: %q", w2.Subdomain)
	}
	if w2.ExpiresAt != w1.ExpiresAt {
		t.Fatalf("resume extended TTL: expires %d, originally %d (#292 F3)",
			w2.ExpiresAt, w1.ExpiresAt)
	}
	c2()
}
