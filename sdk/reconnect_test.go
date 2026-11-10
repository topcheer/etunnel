package etunnel

// Unit tests for the reconnect supervisor's policy pieces. The end-to-end
// behavior (server restart -> same subdomain) is covered by
// reconnect_integration_test.go at the repo root; these tests pin the
// decision functions so a regression cannot hide behind "the integration
// test happened to pass".

import (
	"errors"
	"testing"
	"time"
)

func TestGrowBackoff(t *testing.T) {
	const min, max = 500 * time.Millisecond, 30 * time.Second
	cases := []struct {
		in   time.Duration
		want time.Duration
	}{
		{min, time.Second},
		{time.Second, 2 * time.Second},
		{16 * time.Second, max}, // doubles then caps at max (16*2=32 > 30)
		{20 * time.Second, max}, // capped at max
		{max, max},              // already at ceiling
		{0, max},                // degenerate input saturates
	}
	for _, c := range cases {
		if got := grow(c.in, max); got != c.want {
			t.Errorf("grow(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestJitterStaysWithinBand(t *testing.T) {
	const d = time.Second
	lo, hi := d-d/5, d+d/5+time.Millisecond // +1ms: rand.Int63n bound is inclusive-ish
	for i := 0; i < 500; i++ {
		g := jitter(d)
		if g < lo || g > hi {
			t.Fatalf("jitter(%v) = %v, outside [%v, %v]", d, g, lo, hi)
		}
	}
	if jitter(0) != 0 {
		t.Fatal("jitter(0) must be 0 (no negative sleeps)")
	}
}

func TestIsPermanentClassification(t *testing.T) {
	permanent := []string{"unauthorized", "subdomain_plan", "subdomain_bad", "bad_message"}
	for _, code := range permanent {
		if !isPermanent(&APIError{Code: code, Message: "x"}) {
			t.Errorf("code %q must be permanent (stop retrying)", code)
		}
	}
	transient := []string{"subdomain_taken", "too_many_tunnels", "server_error", ""}
	for _, code := range transient {
		if isPermanent(&APIError{Code: code, Message: "x"}) {
			t.Errorf("code %q must be transient (keep retrying)", code)
		}
	}
	// plain errors (dial failures etc.) are never permanent
	if isPermanent(errors.New("dial tcp: connection refused")) {
		t.Fatal("plain error classified permanent")
	}
	if isPermanent(nil) {
		t.Fatal("nil error classified permanent")
	}
}
