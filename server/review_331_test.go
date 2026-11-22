package server

// #331 review-batch regressions: random-name resume joins the bounded
// acquire (MED), passkeyBegins pruning, obs-text header values, telegram
// sweep-vs-CAS single-send, and O(1) per-account registry counters.

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// #331 LOW2: the passkeyBegins table is pruned when it grows past the floor.
func TestPasskeyBeginsPruned(t *testing.T) {
	s := newBillingTestServer(t)
	for i := 0; i < 5000; i++ {
		s.passkeyBegins[fmt.Sprintf("10.9.%d.%d:1", i/250, i%250)] = &passkeyBeginRate{windowStart: time.Now().Add(-2 * time.Minute), count: 1}
	}
	s.allowPasskeyBegin("fresh:1")
	if len(s.passkeyBegins) > 4096 {
		t.Fatalf("passkeyBegins not pruned: %d entries (#331 LOW2)", len(s.passkeyBegins))
	}
}

// #331 LOW3: obs-text / UTF-8 bytes are legal header values (RFC 7230);
// only CR/LF/NUL and control bytes split responses.
func TestValidHeaderValueAcceptsObsText(t *testing.T) {
	if !validHeaderValue("caf\xc3\xa9 \xe2\x9c\x93") {
		t.Fatal("UTF-8 header value must be accepted (#331 LOW3)")
	}
	if !validHeaderValue("caf\xe9") {
		t.Fatal("obs-text header value must be accepted (#331 LOW3)")
	}
	for _, bad := range []string{"a\r\nSet-Cookie: x", "a\nb", "a\x00b", "a\x01b"} {
		if validHeaderValue(bad) {
			t.Fatalf("control-bearing value %q must be rejected (#331 LOW3)", bad)
		}
	}
	if !validHeaderValue("a\tb") {
		t.Fatal("tab inside a header value is legal")
	}
}

// #331 LOW4: concurrent claimants for one EXPIRED cooldown entry yield
// exactly one notification - the CAS loser's LoadOrStore retry observes the
// winner's fresh window and stays silent.
func TestNotifyTunnelOpenSweepRaceSingleSend(t *testing.T) {
	fake, api := newFakeTelegram(t)
	s := newTGTestServer(t, api, 42)
	tunnelOpenSeen.Range(func(k, _ any) bool {
		tunnelOpenSeen.Delete(k)
		return true
	})
	old := tunnelOpenCooldown
	tunnelOpenCooldown = time.Minute
	t.Cleanup(func() { tunnelOpenCooldown = old })

	// Seed an EXPIRED entry - the sweep/CAS race window state.
	tunnelOpenSeen.Store("race-sub-331", time.Now().Add(-2*tunnelOpenCooldown))

	const n = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s.notifyTunnelOpen(&Session{sub: "race-sub-331", url: "https://race-sub-331.t.test"}, "Starter")
		}()
	}
	close(start)
	wg.Wait()

	time.Sleep(150 * time.Millisecond) // async sends settle
	fake.mu.Lock()
	count := len(fake.sent)
	fake.mu.Unlock()
	if count != 1 {
		t.Fatalf("expired-entry race produced %d notifications, want exactly 1 (#331 LOW4)", count)
	}
}

// #331 LOW5: the O(1) per-account counters stay consistent across
// acquire/release cycles.
func TestAcquireBoundedCounterConsistency(t *testing.T) {
	r := newRegistry()
	acct := &Account{APIKey: "cc"}
	s1 := &Session{id: "a", sub: "n1", account: acct, custom: true, closed: make(chan struct{})}
	if err := r.AcquireBounded("n1", s1, acct, 1, true, 1); err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	// Quota full: a second custom acquire must fail.
	s2 := &Session{id: "b", sub: "n2", account: acct, custom: true, closed: make(chan struct{})}
	if err := r.AcquireBounded("n2", s2, acct, 1, true, 1); err == nil {
		t.Fatal("quota 1 admitted a second custom tunnel")
	}
	// Release the first: counters drop, the retry succeeds (counter-driven).
	r.Release("n1", s1)
	if err := r.AcquireBounded("n2", s2, acct, 1, true, 1); err != nil {
		t.Fatalf("post-release acquire failed - counters drifted: %v (#331 LOW5)", err)
	}
}
