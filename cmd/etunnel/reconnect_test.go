package main

import (
	"testing"
	"time"
)

func TestRetryableClose(t *testing.T) {
	retry := []string{"read closed", "write failed", "ping timeout", "", "some_future_reason"}
	noRetry := []string{"ttl_expired", "too_many_tunnels", "too_many_streams", "limit_reached",
		"unauthorized", "proto_version", "bad_message", "client closed"}

	for _, r := range retry {
		if !retryableClose(r) {
			t.Fatalf("%q should be retryable", r)
		}
	}
	for _, r := range noRetry {
		if retryableClose(r) {
			t.Fatalf("%q must not be retried (policy/configuration failure)", r)
		}
	}
}

func TestBackoffBaseSequence(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, 30 * time.Second}, // ceiling
		{99, 30 * time.Second},
	}
	for _, c := range cases {
		if got := backoffBase(c.attempt, 30*time.Second); got != c.want {
			t.Fatalf("attempt %d = %v, want %v", c.attempt, got, c.want)
		}
	}

	// A custom ceiling is honoured.
	if got := backoffBase(10, 5*time.Second); got != 5*time.Second {
		t.Fatalf("custom max: got %v", got)
	}
	// Zero means the default ceiling, not "no delay".
	if got := backoffBase(10, 0); got != 30*time.Second {
		t.Fatalf("zero max: got %v", got)
	}
}

func TestReconnectDelayJitterBounds(t *testing.T) {
	for attempt := 1; attempt <= 6; attempt++ {
		base := backoffBase(attempt, 30*time.Second)
		lo, hi := base*4/5, base*6/5
		for i := 0; i < 200; i++ {
			d := reconnectDelay(attempt, 30*time.Second)
			if d < lo || d > hi {
				t.Fatalf("attempt %d delay %v outside [%v,%v]", attempt, d, lo, hi)
			}
		}
	}
}
