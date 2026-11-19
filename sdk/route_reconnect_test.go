// route_reconnect_test.go - #229: multi-target mode must be usable through the
// reconnecting client, which the CLI uses by default.
package etunnel

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The regression: ConnectReconnecting rejected an empty LocalAddr outright, but
// -route mode never sets one, so `etunnel -route ...` died with
// "LocalAddr is required" before dialling anything.
func TestConnectReconnectingAllowsRoutesWithoutLocalAddr(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Unreachable server: the call must get as far as dialling. What matters is
	// that it is NOT rejected by argument validation.
	_, err := ConnectReconnecting(ctx, Options{
		Server:              "ws://127.0.0.1:1/tunnel",
		Routes:              []Route{{PathPrefix: "/", Addr: "127.0.0.1:3000"}},
		ReconnectMinBackoff: 10 * time.Millisecond,
		ReconnectMaxBackoff: 20 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected a dial error against an unreachable server")
	}
	if strings.Contains(err.Error(), "LocalAddr is required") {
		t.Fatalf("routing mode rejected by argument validation: %v", err)
	}
}

// The guard still has to reject a call that supplies neither, and a malformed
// route spec must fail fast rather than loop.
func TestConnectReconnectingStillValidatesArguments(t *testing.T) {
	ctx := context.Background()

	if _, err := ConnectReconnecting(ctx, Options{Server: "ws://127.0.0.1:1/tunnel"}); err == nil ||
		!strings.Contains(err.Error(), "LocalAddr is required") {
		t.Fatalf("missing both LocalAddr and Routes: got %v, want the LocalAddr error", err)
	}

	if _, err := ConnectReconnecting(ctx, Options{
		Server: "ws://127.0.0.1:1/tunnel",
		Routes: []Route{{PathPrefix: "no-leading-slash", Addr: "127.0.0.1:3000"}},
	}); err == nil {
		t.Fatal("malformed route spec accepted")
	}
}

// LocalAddr-only callers keep working (unchanged path).
func TestConnectReconnectingStillAcceptsLocalAddr(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := ConnectReconnecting(ctx, Options{
		Server:    "ws://127.0.0.1:1/tunnel",
		LocalAddr: "127.0.0.1:8080",
	})
	if err == nil || strings.Contains(err.Error(), "LocalAddr is required") {
		t.Fatalf("LocalAddr path broke: %v", err)
	}
}
