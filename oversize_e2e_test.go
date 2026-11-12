package etunnel_test

// #14 F#3: an oversize (>64 KiB) text control frame must close with an
// explicit "too large" reason instead of a misleading JSON-decode error
// from the truncated prefix. Uses the real tunnel harness (server read
// loop over a genuine websocket), then injects the oversized frame
// directly on the tunnel's websocket.

import (
	"strings"
	"testing"

	"github.com/coder/websocket"

	sdk "github.com/topcheer/etunnel/sdk"
)

func TestOversizeControlFrameExplicitClose(t *testing.T) {
	_, srvURL := startTestServer(t, nil)
	local := newLocalService(t, nil)
	tun := connectTunnel(t, srvURL, local, func(o *sdk.Options) {
		// keep the raw websocket reachable: the SDK exposes it for tests
		// via DebugConn when built with -tags debugconn; without it we
		// dial our own second tunnel connection instead.
	})
	_ = tun

	// second raw connection: hello, then an oversize control frame
	ctx := t.Context()
	wsURL := sdkURL(srvURL)
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	hello := `{"type":"hello","proto":1,"client":"oversize-test"}`
	if err := c.Write(ctx, websocket.MessageText, []byte(hello)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Read(ctx); err != nil { // welcome
		t.Fatalf("welcome: %v", err)
	}

	big := make([]byte, 64*1024+1)
	for i := range big {
		big[i] = 'a'
	}
	big[0] = '{'
	if err := c.Write(ctx, websocket.MessageText, big); err != nil {
		t.Fatal(err)
	}
	_, _, err = c.Read(ctx)
	if err == nil {
		t.Fatal("connection should have closed on the oversize frame")
	}
	if !strings.Contains(err.Error(), "too large") && !strings.Contains(err.Error(), "1009") {
		t.Fatalf("want explicit too-large close (1009), got: %v", err)
	}
}
