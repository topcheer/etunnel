package server

// Anonymous new-tunnel rate per network prefix (burst guard).
//
// The per-IP limit in ip_limit.go never trips when a client rotates inside
// one operator NAT pool: 8 anonymous tunnels were opened in 40 seconds from
// 94.192.76.1/.2/.3 (same /24), all gone within a minute - a scan/abuse
// probe that the single-IP counter saw as three well-behaved IPs. The rate
// here is therefore keyed on the shared prefix: /24 for IPv4, /64 for IPv6.

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// ip24Window is the rolling window for new anonymous tunnels per prefix.
const ip24Window = time.Minute

// ip24AlertCooldown bounds admin alerts to one per prefix.
const ip24AlertCooldown = 10 * time.Minute

// ip24Prefix returns the shared-network key for ip: the /24 for IPv4, the
// /64 for IPv6. Unparseable addresses key on the raw string so a malformed
// peer never shares a bucket with real traffic.
func ip24Prefix(ip string) string {
	if i := strings.LastIndex(ip, "%"); i >= 0 { // strip IPv6 zone
		ip = ip[:i]
	}
	p := net.ParseIP(ip)
	if p == nil {
		return "raw:" + ip
	}
	if v4 := p.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d", v4[0], v4[1], v4[2])
	}
	v6 := p.To16()
	// Plain []byte: net.IP implements Stringer and fmt would hex-encode the
	// textual form instead of the bytes.
	return fmt.Sprintf("v6:%x", []byte(v6[:8]))
}

// takeIP24Slot enforces the anonymous new-tunnel rate for one handshake's
// network prefix (sliding ip24Window). It returns false when the prefix is
// over the limit: the handshake must be rejected with ErrCodeTooManyTunnels.
//
// Rejected attempts still count, so a hammering client does not get a free
// slot per attempt. Degrades exactly like takeIPSlot (#232): a shared
// counter failure falls back to the process-local counter, with the same
// phantom-INCR compensation; if even the local counter fails, fail closed.
func (s *Server) takeIP24Slot(ctx context.Context, ip string) bool {
	if s.cfg.MaxTunnelsPerIP24PerMin <= 0 || s.counter == nil {
		return true
	}
	pfx := ip24Prefix(ip)
	key := "ip24:" + pfx
	c := s.counter
	n, err := c.Incr(ctx, key, 1)
	if err != nil {
		s.logf("ip %s: shared counter unavailable, degrading /24 rate limit to local: %v", ip, err)
		go func() {
			cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = s.counter.Decr(cctx, key)
		}()
		c = s.localCounter()
		if n, err = c.Incr(ctx, key, 1); err != nil {
			return false
		}
	}
	if n > int64(s.cfg.MaxTunnelsPerIP24PerMin) {
		recordBurst(pfx, ip, int(n))
		s.alertIP24Burst(pfx, ip, int(n), c)
		return false
	}
	_ = c.SetTTL(ctx, key, ip24Window)
	return true
}

// --- admin visibility (/bursts, /unthrottle) -------------------------------
//
// The counter cannot enumerate keys, so rejected prefixes are mirrored in a
// process-local table for the admin bot. It is advisory only: the limit
// itself lives in the counter, so this map may lag or miss entries on other
// nodes without affecting enforcement.

type burstRecord struct {
	Prefix string
	Count  int
	LastIP string
	First  time.Time
	Last   time.Time
}

var burstSeen sync.Map // prefix -> *burstRecord

// burstRetention is how long a throttled prefix stays listed after its last
// rejection (the alert cooldown window).
const burstRetention = ip24AlertCooldown

func recordBurst(pfx, ip string, n int) {
	now := time.Now()
	if v, ok := burstSeen.Load(pfx); ok {
		if r, ok := v.(*burstRecord); ok {
			r.Count, r.LastIP, r.Last = n, ip, now
			return
		}
	}
	burstSeen.Store(pfx, &burstRecord{Prefix: pfx, Count: n, LastIP: ip, First: now, Last: now})
}

// listBursts returns the throttled prefixes, most recent first, dropping
// entries older than burstRetention.
func listBursts() []burstRecord {
	var out []burstRecord
	burstSeen.Range(func(k, v any) bool {
		r, ok := v.(*burstRecord)
		if !ok {
			return true
		}
		if time.Since(r.Last) > burstRetention {
			burstSeen.Delete(k)
			return true
		}
		out = append(out, *r)
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	return out
}

// validIP24Prefix accepts only the shapes ip24Prefix produces, so /unthrottle
// can be pointed at an arbitrary counter key.
func validIP24Prefix(pfx string) bool {
	if strings.HasPrefix(pfx, "v6:") {
		h := strings.TrimPrefix(pfx, "v6:")
		if len(h) != 16 {
			return false
		}
		for _, c := range h {
			if !strings.ContainsRune("0123456789abcdef", c) {
				return false
			}
		}
		return true
	}
	parts := strings.Split(pfx, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" || len(p) > 3 {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// resetIP24 clears the rate window and alert cooldown of one prefix so its
// next handshake is evaluated fresh. The counter has no DEL, so the window is
// unwound by decrementing exactly what was counted.
func (s *Server) resetIP24(ctx context.Context, pfx string) (cleared int64, err error) {
	if s.counter == nil {
		return 0, nil
	}
	key := "ip24:" + pfx
	cleared, err = s.counter.Get(ctx, key)
	if err != nil {
		return 0, err
	}
	for i := int64(0); i < cleared; i++ {
		if err = s.counter.Decr(ctx, key); err != nil {
			return cleared, err
		}
	}
	if n, _ := s.counter.Get(ctx, "ip24alert:"+pfx); n > 0 {
		for i := int64(0); i < n; i++ {
			_ = s.counter.Decr(ctx, "ip24alert:"+pfx)
		}
	}
	burstSeen.Delete(pfx)
	return cleared, nil
}

// claimIP24Alert claims the one admin alert allowed per prefix per cooldown.
// The claim lives in the counter itself, so with a shared backend the dedupe
// is cluster-wide: the first over-limit handshake anywhere pages the admin,
// every later one in the window stays silent.
func claimIP24Alert(ctx context.Context, c Counter, pfx string) bool {
	key := "ip24alert:" + pfx
	n, err := c.Incr(ctx, key, 1)
	if err != nil || n != 1 {
		return false
	}
	_ = c.SetTTL(ctx, key, ip24AlertCooldown)
	return true
}

// alertIP24Burst pages the admin channel (once per prefix per cooldown)
// about an over-limit prefix.
func (s *Server) alertIP24Burst(pfx, ip string, n int, c Counter) {
	if !s.tg.enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !claimIP24Alert(ctx, c, pfx) {
		return
	}
	text := fmt.Sprintf("🛑 tunnel burst: network %s opened more than %d tunnels in 60s (last ip %s%s, count %d) — likely scan/abuse, consider blocking the range",
		pfx, s.cfg.MaxTunnelsPerIP24PerMin, ip, s.geoSuffix(ip), n)
	go s.tgSend(context.Background(), text)
}
