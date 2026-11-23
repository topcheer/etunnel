package server

// Burst guard: anonymous new-tunnel rate per network prefix.
//
// Field case: 8 anonymous tunnels in 40 seconds from 94.192.76.1/.2/.3
// (one Saudi Mobily NAT pool), all gone within a minute. Each IP stayed under
// the per-IP limit, so the single-IP counter never tripped. The rate here is
// keyed on the /24 (IPv4) or /64 (IPv6) instead.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIP24Prefix(t *testing.T) {
	cases := map[string]string{
		"94.192.76.3":          "94.192.76",
		"94.192.76.1":          "94.192.76", // same pool, same bucket
		"94.192.77.1":          "94.192.77", // neighbour /24 is a different bucket
		"::ffff:94.192.76.3":   "94.192.76", // IPv4-mapped folds onto the v4 bucket
		"2001:db8:1:2::1":      "v6:20010db800010002",
		"2001:db8:1:2:ffff::1": "v6:20010db800010002", // same /64
		"2001:db8:1:3::1":      "v6:20010db800010003",
		"fe80::1%en0":          "v6:fe80000000000000", // zone stripped
		"not-an-ip":            "raw:not-an-ip",
	}
	for ip, want := range cases {
		if got := ip24Prefix(ip); got != want {
			t.Errorf("ip24Prefix(%q) = %q, want %q", ip, got, want)
		}
	}
}

func TestIP24RateRejectsBurstAcrossPool(t *testing.T) {
	s := &Server{cfg: Config{MaxTunnelsPerIP24PerMin: 3}, counter: newMemCounter()}
	ctx := context.Background()

	// Three different IPs in one /24, each well under any per-IP limit:
	// the first three handshakes are granted, the fourth is rejected.
	for i, ip := range []string{"94.192.76.1", "94.192.76.2", "94.192.76.3"} {
		if !s.takeIP24Slot(ctx, ip) {
			t.Fatalf("handshake %d from %s rejected under the limit", i+1, ip)
		}
	}
	if s.takeIP24Slot(ctx, "94.192.76.3") {
		t.Fatal("4th tunnel in the window from the same /24 granted: burst not enforced")
	}
	// Rejected attempts keep counting: no free slot per retry.
	if s.takeIP24Slot(ctx, "94.192.76.1") {
		t.Fatal("retry after rejection granted")
	}
	// A different /24 is unaffected.
	if !s.takeIP24Slot(ctx, "94.192.77.9") {
		t.Fatal("neighbour /24 throttled by an unrelated burst")
	}
}

func TestIP24RateDisabledAndNoCounter(t *testing.T) {
	ctx := context.Background()
	off := &Server{cfg: Config{MaxTunnelsPerIP24PerMin: 0}, counter: newMemCounter()}
	for i := 0; i < 50; i++ {
		if !off.takeIP24Slot(ctx, "94.192.76.1") {
			t.Fatal("limit 0 must mean unlimited")
		}
	}
	noCounter := &Server{cfg: Config{MaxTunnelsPerIP24PerMin: 1}}
	if !noCounter.takeIP24Slot(ctx, "94.192.76.1") {
		t.Fatal("nil counter (single node without a counter) must not reject")
	}
}

func TestIP24RateDegradesInsteadOfFailingOpen(t *testing.T) {
	// Same contract as takeIPSlot (#232): a dead shared counter degrades to
	// the process-local counter and the limit stays enforced.
	s := &Server{cfg: Config{MaxTunnelsPerIP24PerMin: 1}, counter: newFlakyCounter()}
	ctx := context.Background()
	if !s.takeIP24Slot(ctx, "94.192.76.1") {
		t.Fatal("first handshake rejected while degraded")
	}
	if s.takeIP24Slot(ctx, "94.192.76.2") {
		t.Fatal("same /24 burst granted while degraded: limit vanished")
	}
	select {
	case k := <-s.counter.(*flakyCounter).decrs:
		if k != "ip24:94.192.76" {
			t.Fatalf("phantom compensation hit %q", k)
		}
	case <-time.After(time.Second):
		t.Fatal("phantom INCR on the shared counter was not compensated")
	}
}

func TestIP24AlertClaimedOnce(t *testing.T) {
	c := newMemCounter()
	ctx := context.Background()
	if !claimIP24Alert(ctx, c, "94.192.76") {
		t.Fatal("first claim denied")
	}
	for i := 0; i < 5; i++ {
		if claimIP24Alert(ctx, c, "94.192.76") {
			t.Fatal("second alert for the same prefix inside the cooldown")
		}
	}
	if !claimIP24Alert(ctx, c, "94.192.77") {
		t.Fatal("claim for another prefix blocked by an unrelated cooldown")
	}
}

func clearBursts() {
	burstSeen.Range(func(k, _ any) bool { burstSeen.Delete(k); return true })
}

func TestIP24BurstRegistryAndReset(t *testing.T) {
	clearBursts() // package-level table shared across tests
	s := &Server{cfg: Config{MaxTunnelsPerIP24PerMin: 1}, counter: newMemCounter()}
	ctx := context.Background()
	if len(listBursts()) != 0 {
		t.Fatal("fresh process lists bursts")
	}
	s.takeIP24Slot(ctx, "94.192.76.1") // granted
	if s.takeIP24Slot(ctx, "94.192.76.2") {
		t.Fatal("second handshake granted")
	}
	s.takeIP24Slot(ctx, "94.192.76.3") // rejected again: count refreshes
	got := listBursts()
	if len(got) != 1 || got[0].Prefix != "94.192.76" || got[0].LastIP != "94.192.76.3" || got[0].Count != 3 {
		t.Fatalf("burst registry = %+v", got)
	}

	// /unthrottle validation: only shapes ip24Prefix produces.
	for _, bad := range []string{"", "94.192", "94.192.76.1", "ip24:x", "v6:zz", "1.2.3 "} {
		if validIP24Prefix(bad) {
			t.Errorf("validIP24Prefix(%q) accepted", bad)
		}
	}
	for _, ok := range []string{"94.192.76", "0.0.0", "v6:20010db800010002"} {
		if !validIP24Prefix(ok) {
			t.Errorf("validIP24Prefix(%q) rejected", ok)
		}
	}

	cleared, err := s.resetIP24(ctx, "94.192.76")
	if err != nil || cleared != 3 {
		t.Fatalf("reset cleared=%d err=%v", cleared, err)
	}
	if len(listBursts()) != 0 {
		t.Fatal("reset left the registry entry")
	}
	if !s.takeIP24Slot(ctx, "94.192.76.1") {
		t.Fatal("handshake still rejected after /unthrottle")
	}
	// The alert cooldown was cleared too: a fresh burst pages again.
	if !claimIP24Alert(ctx, s.counter, "94.192.76") {
		t.Fatal("alert cooldown survived reset")
	}
}

func TestIP24AdminCommands(t *testing.T) {
	clearBursts()
	s := &Server{cfg: Config{MaxTunnelsPerIP24PerMin: 1}, counter: newMemCounter()}
	if out := s.handleAdminCommand("/bursts"); !strings.HasPrefix(out, "no network") {
		t.Fatalf("empty /bursts = %q", out)
	}
	if out := s.handleAdminCommand("/unthrottle 94.192.76.1"); !strings.HasPrefix(out, "usage:") {
		t.Fatalf("invalid /unthrottle = %q", out)
	}
	s.takeIP24Slot(context.Background(), "94.192.76.1")
	s.takeIP24Slot(context.Background(), "94.192.76.2")
	out := s.handleAdminCommand("/bursts")
	if !strings.Contains(out, "94.192.76 · 2 in window · last 94.192.76.2") {
		t.Fatalf("/bursts = %q", out)
	}
	body, kb := s.handleAdminCommandKB("/bursts")
	if !strings.Contains(body, "tap a network to unthrottle") || len(kb) < 1 || len(kb[0]) != 1 || kb[0][0] != "/unthrottle 94.192.76" {
		t.Fatalf("/bursts keyboard = %q %v", body, kb)
	}
	if out := s.handleAdminCommand("/unthrottle 94.192.76"); !strings.Contains(out, "unthrottled 94.192.76 (cleared 2") {
		t.Fatalf("/unthrottle = %q", out)
	}
	if out := s.handleAdminCommand("/help"); !strings.Contains(out, "/bursts") || !strings.Contains(out, "/unthrottle") {
		t.Fatal("/help does not list the burst commands")
	}
}

func TestIP24AlertPagesAdminOnce(t *testing.T) {
	sent := make(chan string, 8)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sent <- body.Text
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer fake.Close()

	s := &Server{
		cfg:     Config{MaxTunnelsPerIP24PerMin: 2},
		counter: newMemCounter(),
		tg:      telegramConfig{token: "TEST:TOKEN", chatID: 1, apiBase: fake.URL},
	}
	ctx := context.Background()
	for _, ip := range []string{"94.192.76.1", "94.192.76.2"} {
		if !s.takeIP24Slot(ctx, ip) {
			t.Fatalf("%s rejected under the limit", ip)
		}
	}
	for _, ip := range []string{"94.192.76.3", "94.192.76.1", "94.192.76.2"} {
		if s.takeIP24Slot(ctx, ip) {
			t.Fatalf("%s granted over the limit", ip)
		}
	}

	select {
	case text := <-sent:
		for _, want := range []string{"94.192.76", "94.192.76.3"} {
			if !strings.Contains(text, want) {
				t.Fatalf("alert %q lacks %q", text, want)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no admin alert delivered")
	}
	select {
	case text := <-sent:
		t.Fatalf("second alert inside the cooldown: %q", text)
	case <-time.After(300 * time.Millisecond):
	}
}
