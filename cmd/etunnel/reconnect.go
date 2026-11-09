package main

import (
	"math/rand"
	"time"
)

// Reconnect policy for the CLI (issue #15).
//
// The SDK deliberately has no reconnect: a library that silently retries can
// hide outages from its caller, and the 20-language SDKs must agree on the
// API before any of them grows one. A CLI user's expectation is the opposite -
// a long-TTL tunnel should survive a network blip - so the policy lives here,
// on top of the SDK, where it can change without a protocol or API decision.

// transientCloseReasons are close reasons worth retrying: transport failures
// and a server that went away (restart, deploy, dropped connection).
func retryableClose(reason string) bool {
	switch reason {
	case "read closed", "write failed", "ping timeout",
		// The server sends an empty or unknown reason when it is shutting down
		// or the socket died before a bye arrived.
		"":
		return true
	}
	// Policy or configuration failures: retrying cannot help and may make it
	// worse (rate limits, expired plan, bad token, version mismatch).
	switch reason {
	case "ttl_expired", "too_many_tunnels", "too_many_streams", "limit_reached",
		"unauthorized", "proto_version", "bad_message", "client closed":
		return false
	}
	// An unrecognised reason comes from a newer server; treat it as transient
	// rather than killing a working tunnel on an unknown string.
	return true
}

// backoffBase is the delay before reconnect attempt n (1-based): 1s, 2s, 4s,
// 8s, 16s, then the ceiling. Uncapped exponential growth would make a
// five-minute outage cost hours of downtime.
func backoffBase(attempt int, max time.Duration) time.Duration {
	if max <= 0 {
		max = 30 * time.Second
	}
	d := time.Second
	for i := 1; i < attempt && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	return d
}

// reconnectDelay adds +/-20% jitter so a fleet of clients that lost the same
// server does not reconnect in lockstep.
func reconnectDelay(attempt int, max time.Duration) time.Duration {
	base := backoffBase(attempt, max)
	spread := base / 5 // +/-20%
	jitter := time.Duration(rand.Int63n(int64(2*spread)+1)) - spread
	if d := base + jitter; d > 0 {
		return d
	}
	return base
}
