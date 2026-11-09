package server

import (
	"testing"
)

// Issue #10: a single API key must not be able to hold an unbounded number of
// concurrent tunnels.

func newCapTestServer(t *testing.T) *Server {
	t.Helper()
	srv, err := New(Config{Domain: "t.test"})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// addTunnel registers a live session for the given account key in the registry.
func addTunnel(srv *Server, sub, apiKey string, closed bool) *Session {
	sess := &Session{
		sub:     sub,
		closed:  make(chan struct{}),
		streams: make(map[uint32]*Stream),
	}
	if apiKey != "" {
		sess.account = &Account{APIKey: apiKey, Email: "a@x.com"}
	}
	if closed {
		close(sess.closed)
	}
	srv.reg.subs[sub] = sess
	return sess
}

func TestRegistryCountsLiveTunnelsPerAccount(t *testing.T) {
	srv := newCapTestServer(t)
	addTunnel(srv, "a1", "et_key1", false)
	addTunnel(srv, "a2", "et_key1", false)
	addTunnel(srv, "a3", "et_key1", true) // closed: still mapped, must not count
	addTunnel(srv, "b1", "et_key2", false)
	addTunnel(srv, "anon1", "", false)

	if got := srv.reg.countForAccount("et_key1"); got != 2 {
		t.Fatalf("key1 live tunnels = %d, want 2", got)
	}
	if got := srv.reg.countForAccount("et_key2"); got != 1 {
		t.Fatalf("key2 live tunnels = %d, want 1", got)
	}
	if got := srv.reg.countForAccount(""); got != 0 {
		t.Fatalf("empty key must not match anonymous tunnels, got %d", got)
	}
}

func TestAccountTunnelLimitGate(t *testing.T) {
	srv := newCapTestServer(t)
	acct := &Account{APIKey: "et_key1", Email: "a@x.com"}
	addTunnel(srv, "a1", "et_key1", false)
	addTunnel(srv, "a2", "et_key1", false)

	cases := []struct {
		name  string
		acct  *Account
		tier  *Tier
		limit bool
	}{
		{"at limit", acct, &Tier{Name: "Free", MaxTunnels: 2}, true},
		{"below limit", acct, &Tier{Name: "Pro", MaxTunnels: 20}, false},
		{"unlimited tier", acct, &Tier{Name: "Legacy", MaxTunnels: 0}, false},
		{"anonymous", nil, &Tier{Name: "Free", MaxTunnels: 2}, false},
	}
	for _, tc := range cases {
		if got := srv.accountTunnelLimitReached(tc.acct, tc.tier); got != tc.limit {
			t.Fatalf("%s: reached = %v, want %v", tc.name, got, tc.limit)
		}
	}
}

// Default plans must cap concurrency, otherwise #10 stays open for every key.
func TestDefaultTiersCapTunnels(t *testing.T) {
	srv := newCapTestServer(t)
	want := map[string]int{"free": 2, "starter": 5, "pro": 20}
	for name, n := range want {
		tier, ok := srv.cfg.Tiers[name]
		if !ok {
			t.Fatalf("tier %s missing", name)
		}
		if tier.MaxTunnels != n {
			t.Fatalf("tier %s MaxTunnels = %d, want %d", name, tier.MaxTunnels, n)
		}
	}
}
