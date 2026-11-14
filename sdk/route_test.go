package etunnel

import (
	"strings"
	"testing"
)

func mustRoutes(t *testing.T, rs []Route) []*compiledRoute {
	t.Helper()
	out, err := normalizeRoutes(rs)
	if err != nil {
		t.Fatalf("normalizeRoutes: %v", err)
	}
	return out
}

func TestRouteMatchSegmentBoundaries(t *testing.T) {
	rs := mustRoutes(t, []Route{
		{PathPrefix: "/", Addr: "fe:1"},
		{PathPrefix: "/api", Addr: "be:1"},
		{PathPrefix: "/api/v2", Addr: "be2:1"},
	})
	cases := []struct {
		path string
		addr string
	}{
		{"/", "fe:1"},
		{"/index.html", "fe:1"},
		{"/apiv2/x", "fe:1"},   // segment boundary: /api must NOT match
		{"/apiary", "fe:1"},    // ditto
		{"/api", "be:1"},       // exact
		{"/api/", "be:1"},      // trailing slash variant
		{"/api/users", "be:1"}, // nested
		{"/api/v2", "be2:1"},   // longest prefix wins over /api
		{"/api/v2/x?y=1", "be2:1"},
		{"/api-v2", "fe:1"}, // hyphen is not a boundary
	}
	for _, c := range cases {
		addr, _, ok := routeFor(rs, c.path)
		if !ok || addr != c.addr {
			t.Errorf("routeFor(%q) = %q,%v; want %q", c.path, addr, ok, c.addr)
		}
	}
}

func TestRouteStrip(t *testing.T) {
	rs := mustRoutes(t, []Route{
		{PathPrefix: "/api", Addr: "be:1", StripPrefix: true},
		{PathPrefix: "/", Addr: "fe:1", StripPrefix: true}, // strip on "/" = no-op
	})
	cases := []struct{ in, out string }{
		{"/api/users?x=1&y=2", "/users?x=1&y=2"}, // query preserved verbatim
		{"/api", "/"},                            // exact match bottoms out at root
		{"/api/", "/"},                           // trailing slash also root
		{"/api/deep/nested?q", "/deep/nested?q"}, // multi-segment
		{"/app?cfg=1", "/app?cfg=1"},             // strip "/" no-op, query intact
		{"/", "/"},
		{"/?only=query", "/?only=query"},
	}
	for _, c := range cases {
		addr, got, ok := routeFor(rs, c.in)
		if !ok {
			t.Fatalf("routeFor(%q): no match", c.in)
		}
		_ = addr
		if got != c.out {
			t.Errorf("strip %q -> %q; want %q", c.in, got, c.out)
		}
	}
}

func TestRoutePassthroughNoStrip(t *testing.T) {
	rs := mustRoutes(t, []Route{
		{PathPrefix: "/api", Addr: "be:1"}, // no strip: path untouched
		{PathPrefix: "/", Addr: "fe:1"},
	})
	for _, p := range []string{"/api", "/api/users?x=1", "/api/"} {
		addr, got, ok := routeFor(rs, p)
		if !ok || got != p || addr != "be:1" {
			t.Errorf("passthrough %q -> (%q,%q,%v); want path unchanged to be:1", p, addr, got, ok)
		}
	}
}

func TestRouteNoMatch(t *testing.T) {
	// no fallback "/" route: unmatched paths must report !ok (caller 404s)
	rs := mustRoutes(t, []Route{{PathPrefix: "/api", Addr: "be:1"}})
	if _, _, ok := routeFor(rs, "/other"); ok {
		t.Fatal("/other matched a /api-only table")
	}
	// while /api still matches
	if _, _, ok := routeFor(rs, "/api/x"); !ok {
		t.Fatal("/api/x must match")
	}
}

func TestRouteNormalizeTrailingSlash(t *testing.T) {
	rs := mustRoutes(t, []Route{{PathPrefix: "/api/", Addr: "be:1"}})
	if rs[0].prefix != "/api" {
		t.Fatalf("prefix not normalized: %q", rs[0].prefix)
	}
	if _, _, ok := routeFor(rs, "/api/x"); !ok {
		t.Fatal("normalized prefix fails to match")
	}
}

func TestRouteValidation(t *testing.T) {
	cases := []struct {
		name   string
		routes []Route
		substr string
	}{
		{"empty addr", []Route{{PathPrefix: "/"}}, "Addr is required"},
		{"empty prefix", []Route{{Addr: "a:1"}}, "PathPrefix is required"},
		{"no leading slash", []Route{{PathPrefix: "api", Addr: "a:1"}}, "must start with '/'"},
		{"duplicate prefix", []Route{
			{PathPrefix: "/api", Addr: "a:1"},
			{PathPrefix: "/api/", Addr: "a:2"}, // same route after normalization
		}, "duplicate"},
		{"dup vs root", []Route{
			{PathPrefix: "/", Addr: "a:1"},
			{PathPrefix: "//", Addr: "a:2"}, // "//" normalizes to "/"
		}, "duplicate"},
	}
	for _, c := range cases {
		_, err := normalizeRoutes(c.routes)
		if err == nil || !strings.Contains(err.Error(), c.substr) {
			t.Errorf("%s: err=%v; want substring %q", c.name, err, c.substr)
		}
	}
	if rs, err := normalizeRoutes(nil); err != nil || rs != nil {
		t.Errorf("nil routes must stay legacy (nil,nil); got (%v,%v)", rs, err)
	}
}
