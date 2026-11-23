package server

// Offline IP geolocation for admin notifications (ip2region xdb files).
//
// Scope: lookups happen ONLY inside the async Telegram push paths - the
// tunnel-open notification and the burst-guard alert - never on the
// handshake or proxy path. The data is a local xdb file (v4 and/or v6), so
// no visitor IP ever leaves the box and there is no third-party rate limit.
//
// xdb.Searcher is not safe for concurrent use (file-based search seeks a
// shared handle), so one mutex guards both searchers; notification volume
// is a few per minute at most, so a lock is the whole pool needed.

import (
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
)

// geoLookup resolves one IP to a raw ip2region region string
// ("country|region|province|city|isp") or "" when unknown. It is an
// interface so tests can drive the notification formatting without an xdb.
type geoLookup interface {
	lookup(ip string) string
}

// GeoIP wraps the ip2region searchers loaded from the configured xdb files.
type GeoIP struct {
	mu sync.Mutex
	v4 *xdb.Searcher
	v6 *xdb.Searcher
}

// newGeoIP loads the v4 and v6 xdb files; either path may be empty. With
// both empty it returns (nil, nil): geolocation is simply disabled. A file
// that is set but unreadable or of the wrong IP version is a startup error,
// so a typo in the flag cannot silently disable the feature.
func newGeoIP(v4Path, v6Path string) (*GeoIP, error) {
	g := &GeoIP{}
	if v4Path != "" {
		s, err := xdb.NewWithFileOnly(xdb.IPv4, v4Path)
		if err != nil {
			return nil, fmt.Errorf("geoip v4 %s: %w", v4Path, err)
		}
		g.v4 = s
	}
	if v6Path != "" {
		s, err := xdb.NewWithFileOnly(xdb.IPv6, v6Path)
		if err != nil {
			g.Close()
			return nil, fmt.Errorf("geoip v6 %s: %w", v6Path, err)
		}
		g.v6 = s
	}
	if g.v4 == nil && g.v6 == nil {
		return nil, nil
	}
	return g, nil
}

// Close releases the xdb file handles.
func (g *GeoIP) Close() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.v4 != nil {
		g.v4.Close()
		g.v4 = nil
	}
	if g.v6 != nil {
		g.v6.Close()
		g.v6 = nil
	}
}

// lookup resolves ip against the matching searcher. Unparseable input and
// IPs of a version whose file was not configured resolve to "".
func (g *GeoIP) lookup(ip string) string {
	if g == nil || ip == "" {
		return ""
	}
	if i := strings.LastIndex(ip, "%"); i >= 0 { // strip IPv6 zone
		ip = ip[:i]
	}
	p := net.ParseIP(ip)
	if p == nil {
		return ""
	}
	var (
		s   *xdb.Searcher
		key string
	)
	if p4 := p.To4(); p4 != nil {
		s, key = g.v4, p4.String() // IPv4-mapped v6 folds onto the v4 searcher
	} else {
		s, key = g.v6, p.String()
	}
	if s == nil {
		return ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	region, err := s.Search(key)
	if err != nil || region == "" {
		return ""
	}
	return region
}

// formatGeoRegion turns "国家|区域|省份|城市|ISP" into a compact label:
// ip2region pads missing fields with "0", some rows repeat the province as
// the city, and the v3 files append a 2-letter ISO country code after the
// country name; all three are dropped. Fields are joined with " · ".
func formatGeoRegion(region string) string {
	parts := strings.Split(region, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || p == "0" {
			continue
		}
		if isISOCountryCode(p) && len(out) > 0 {
			continue // trailing code duplicates the country name
		}
		if n := len(out); n > 0 && out[n-1] == p {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, " · ")
}

// isISOCountryCode reports whether s is exactly two ASCII capitals - the
// shape of the ISO 3166-1 alpha-2 suffix ip2region v3 appends.
func isISOCountryCode(s string) bool {
	return len(s) == 2 && s[0] >= 'A' && s[0] <= 'Z' && s[1] >= 'A' && s[1] <= 'Z'
}

// geoLabel returns the formatted region for ip, or "" when geolocation is
// disabled or the IP is unknown.
func (s *Server) geoLabel(ip string) string {
	if s == nil || s.geo == nil {
		return ""
	}
	return formatGeoRegion(s.geo.lookup(ip))
}

// geoSuffix is the " (region)" fragment appended to IP mentions in admin
// messages; "" when there is nothing to add.
func (s *Server) geoSuffix(ip string) string {
	if l := s.geoLabel(ip); l != "" {
		return " (" + l + ")"
	}
	return ""
}
