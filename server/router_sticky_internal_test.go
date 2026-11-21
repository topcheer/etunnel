package server

// No-Redis regression guard for the #295 forward-header trust boundary.
//
// The v1.7.28 tag CI failed because TestStickyRouterLive's loop-guard leg
// sent X-Etunnel-Forward from httptest's default RemoteAddr (192.0.2.1,
// TEST-NET-1): isInternalPeer classifies it PUBLIC, so Forward stripped the
// forged header and the guard never fired (PR #319 realigned that live test).
// This table test pins the predicate itself so the classification that
// decides strip-vs-honor cannot silently drift - no live Redis needed.

import "testing"

func TestIsInternalPeer(t *testing.T) {
	cases := []struct {
		addr string
		want bool
		why  string
	}{
		{"127.0.0.1:4444", true, "loopback v4"},
		{"[::1]:4444", true, "loopback v6"},
		{"10.0.0.9:8080", true, "RFC1918 10/8 (cluster peer)"},
		{"172.16.5.4:8080", true, "RFC1918 172.16/12"},
		{"192.168.1.20:8080", true, "RFC1918 192.168/16 (LB)"},
		{"192.0.2.1:1234", false, "TEST-NET-1 is public - httptest default"},
		{"203.0.113.9:4444", false, "TEST-NET-3 visitor"},
		{"8.8.8.8:53", false, "internet"},
		{"not-an-ip:80", false, "unparseable host"},
		{"", false, "empty"},
	}
	for _, c := range cases {
		if got := isInternalPeer(c.addr); got != c.want {
			t.Errorf("isInternalPeer(%q) = %v, want %v (%s)", c.addr, got, c.want, c.why)
		}
	}
}
