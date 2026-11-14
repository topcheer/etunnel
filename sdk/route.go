package etunnel

import (
	"fmt"
	"sort"
	"strings"
)

// Route maps one URL path prefix of the public tunnel to one local address.
// A tunnel with Routes serves several local services under the same public
// hostname (e.g. "/" to a built SPA and "/api" to its backend), which keeps
// the deployment single-origin. Routing is client-side: the server keeps
// forwarding by hostname; the SDK picks the local target per request path.
type Route struct {
	// PathPrefix is the public URL path segment this route serves, e.g.
	// "/api". Must start with "/". "/" is the catch-all fallback. Matching
	// is on segment boundaries: "/api" matches "/api" and "/api/..." but
	// never "/apiv2".
	PathPrefix string
	// Addr is the local service address ("127.0.0.1:8080").
	Addr string
	// StripPrefix removes the matched prefix from the path before the
	// request reaches Addr. The query string is preserved untouched; an
	// exact-prefix match ("/api") becomes "/". Typical use: an SPA dev
	// server proxying "/api" locally emits backend routes without the
	// prefix, and the prod bundle expects the same shape.
	StripPrefix bool
}

// ValidateRoutes checks a Routes slice with the same rules Connect applies
// (prefix shape, addresses, duplicates). CLI-level parsers use it to fail
// fast on bad flags instead of at connect time.
func ValidateRoutes(routes []Route) error {
	_, err := normalizeRoutes(routes)
	return err
}

// compiled route, pre-normalized, sorted longest-prefix-first.
type compiledRoute struct {
	prefix string // no trailing slash (except "/")
	Route
}

// normalizeRoutes validates Routes and compiles them for matching. Empty
// input yields nil (legacy single-target mode).
func normalizeRoutes(routes []Route) ([]*compiledRoute, error) {
	if len(routes) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(routes))
	out := make([]*compiledRoute, 0, len(routes))
	for i, r := range routes {
		if r.Addr == "" {
			return nil, fmt.Errorf("etunnel: route[%d]: Addr is required", i)
		}
		p := r.PathPrefix
		if p == "" {
			return nil, fmt.Errorf("etunnel: route[%d]: PathPrefix is required (use \"/\" for the fallback route)", i)
		}
		if p[0] != '/' {
			return nil, fmt.Errorf("etunnel: route[%d]: PathPrefix %q must start with '/'", i, p)
		}
		// Normalize: collapse a trailing slash ("/api/" -> "/api"); "/" stays.
		for len(p) > 1 && strings.HasSuffix(p, "/") {
			p = strings.TrimSuffix(p, "/")
		}
		if seen[p] {
			return nil, fmt.Errorf("etunnel: route[%d]: duplicate PathPrefix %q", i, p)
		}
		seen[p] = true
		r.PathPrefix = p
		out = append(out, &compiledRoute{prefix: p, Route: r})
	}
	// Longest prefix first so "/api/v2" beats "/api" beats "/" without any
	// special-casing at match time.
	sort.SliceStable(out, func(a, b int) bool { return len(out[a].prefix) > len(out[b].prefix) })
	return out, nil
}

// routeFor resolves the request path (path+query, protocol.Req shape) to a
// target address and the possibly-stripped path. ok=false means no route
// matched (caller answers 404).
func routeFor(routes []*compiledRoute, pathAndQuery string) (addr, path string, ok bool) {
	// Split path from query once; matching and stripping touch only the
	// path part, the query is carried over byte-for-byte.
	p := pathAndQuery
	q := ""
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p, q = p[:i], p[i:]
	}
	for _, r := range routes {
		if !prefixMatchesSegment(p, r.prefix) {
			continue
		}
		out := p
		if r.StripPrefix {
			out = stripPrefix(p, r.prefix)
		}
		if q != "" {
			out += q
		}
		return r.Addr, out, true
	}
	return "", "", false
}

// prefixMatchesSegment reports whether path is prefix itself or extends it
// by a "/" segment boundary ("/api" vs "/apiv2" must not match).
func prefixMatchesSegment(path, prefix string) bool {
	if prefix == "/" {
		return true // catch-all; a non-/ first segment simply starts with /
	}
	if path == prefix {
		return true
	}
	return strings.HasPrefix(path, prefix+"/")
}

// stripPrefix removes prefix from path on segment boundaries; the result is
// always a rooted path. A "/" prefix owns everything, so stripping it is a
// no-op (the path is already rooted) - it must NOT collapse to "/".
func stripPrefix(path, prefix string) string {
	if prefix == "/" {
		return path // owns everything: stripping the root is a no-op
	}
	if path == prefix {
		return "/"
	}
	return path[len(prefix):]
}
