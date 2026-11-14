// WSS source-IP bans for tunnel establishment (owner directive 2026-11-14):
// the public wildcard DNS is direct (no Cloudflare in front), so any WSS
// connect arriving FROM a Cloudflare range is a client hosted inside
// CF Workers/WARP - observed as ~2-minute open/close churning loops that
// squat free-tier tunnel slots. The default ban list is Cloudflare's
// published ranges (https://www.cloudflare.com/ips); ETUNNEL_BAN_RANGES
// overrides it with a comma-separated CIDR list ("-" disables).
//
// This gates TUNNEL ESTABLISHMENT only - visitor HTTP traffic to existing
// tunnels is untouched (a visitor who happens to egress via WARP is not
// the target here; that is allowIPs' domain).
package server

import (
	"net"
	"os"
	"strings"
	"sync"
)

// cloudflareRanges is the published Cloudflare list (v4 + v6), snapshotted
// so cold starts need no outbound fetch. Cloudflare updates it rarely;
// override via ETUNNEL_BAN_RANGES when it drifts.
var cloudflareRanges = []string{
	// Published edge list (https://www.cloudflare.com/ips-v4 + ips-v6):
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
	"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
	"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
	"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
	// NOT in the published list but observed in production as Cloudflare
	// Worker/WARP egress (session logs 2026-11-14, AS13335): 104.28.x
	// clients churning tunnel slots. Covered with /14, all CF space.
	"104.28.0.0/14",
}

// wssBans parses the effective ban list: ETUNNEL_BAN_RANGES overrides the
// Cloudflare snapshot ("-" disables, comma-separated CIDRs otherwise).
// Misconfiguration must not be silent (#143): when the env var is set but
// nothing parses, the operator gets one loud log line per process (the
// result is cached), and the fall back is the Cloudflare snapshot rather
// than an empty list. Per-CIDR drops are counted in the same line.
var (
	banCacheMu    sync.Mutex
	banCache      []*net.IPNet
	banCacheBuilt bool
)

func (s *Server) wssBans() []*net.IPNet {
	banCacheMu.Lock()
	defer banCacheMu.Unlock()
	if banCacheBuilt {
		return banCache
	}
	spec := strings.TrimSpace(os.Getenv("ETUNNEL_BAN_RANGES"))
	var ranges []string
	switch {
	case spec == "" || spec == "cf":
		ranges = cloudflareRanges
	case spec == "-":
		banCache, banCacheBuilt = nil, true
		return nil
	default:
		ranges = strings.Split(spec, ",")
	}
	var out []*net.IPNet
	for _, c := range ranges {
		if _, n, err := net.ParseCIDR(strings.TrimSpace(c)); err == nil {
			out = append(out, n)
		}
	}
	if spec != "" && spec != "cf" && len(out) == 0 {
		if s != nil {
			s.logf("wss: ETUNNEL_BAN_RANGES=%q parsed to zero CIDRs - falling back to the Cloudflare snapshot (check separators: comma-separated)", spec)
		}
		out = parseCIDRs(cloudflareRanges)
	} else if spec != "" && spec != "cf" && len(out) < len(ranges) {
		if s != nil {
			s.logf("wss: ETUNNEL_BAN_RANGES: %d of %d CIDRs failed to parse and were skipped", len(ranges)-len(out), len(ranges))
		}
	}
	banCache, banCacheBuilt = out, true
	return out
}

func parseCIDRs(rs []string) []*net.IPNet {
	var out []*net.IPNet
	for _, c := range rs {
		if _, n, err := net.ParseCIDR(strings.TrimSpace(c)); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// banWSS reports whether the WSS peer address is in the ban list.
func banWSS(s *Server, remoteAddr string) bool {
	bans := s.wssBans()
	if len(bans) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range bans {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
