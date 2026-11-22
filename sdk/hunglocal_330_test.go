package etunnel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/topcheer/etunnel/protocol"
)

// hungLocalConn stands in for a 101-hijacked local connection whose app is
// hung: it consumes request bytes, never writes a response, and never FINs.
type hungLocalConn struct {
	mu       sync.Mutex
	closed   bool
	closedCh chan struct{}
	received []byte
}

func (c *hungLocalConn) Read(p []byte) (int, error) {
	<-c.closedCh // the local app is hung: it never produces output
	return 0, errors.New("etunnel: conn closed by tunnel teardown")
}

func (c *hungLocalConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.received = append(c.received, p...)
	c.mu.Unlock()
	return len(p), nil
}

// CloseWrite is present (real-TCP shape): a half close must NOT close the
// whole connection - that is exactly the #330-1 leak scenario.
func (c *hungLocalConn) CloseWrite() error { return nil }

func (c *hungLocalConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.closedCh)
	}
	return nil
}

// #330-1 (HIGH): serveLocalUpgrade must not park forever on the second
// direction when the local service is hung and never FINs - tunnel teardown
// (ctx cancellation) has to actively close rwc and reclaim the handler
// goroutine and the stream slot.
func TestServeLocalUpgradeHungLocalReclaimsOnTunnelClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tun := &Tunnel{
		ctx:     ctx,
		cancel:  cancel,
		out:     make(chan wsOut, 8),
		closed:  make(chan struct{}),
		streams: make(map[uint32]*clientStream),
	}

	sctx, scancel := context.WithCancel(ctx)
	st := &clientStream{
		id:     1,
		t:      tun,
		ctx:    sctx,
		cancel: scancel,
		frames: make(chan protocol.Frame, 8),
		done:   make(chan struct{}),
	}
	tun.mu.Lock()
	tun.streams[1] = st
	tun.mu.Unlock()
	st.reqPr, st.reqPw = io.Pipe()

	conn := &hungLocalConn{closedCh: make(chan struct{})}
	resp := &http.Response{Header: http.Header{}, StatusCode: http.StatusSwitchingProtocols, Body: conn}

	served := make(chan struct{})
	go func() {
		defer close(served)
		tun.serveLocalUpgrade(st, resp)
	}()
	go st.pump()

	// visitor sends a byte, then FIN (pump delivers reqPr EOF)
	st.frames <- protocol.Frame{Kind: protocol.KindData, StreamID: 1, Payload: []byte("V")}
	st.frames <- protocol.Frame{Kind: protocol.KindFin, StreamID: 1}

	// let the request byte reach the hung local and the stream park
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn.mu.Lock()
		got := len(conn.received)
		conn.mu.Unlock()
		if got > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond) // old code parks here forever (leak)

	// tunnel teardown: ctx cancellation must actively close rwc and unwind
	cancel()

	select {
	case <-served:
		// serveLocalUpgrade returned: handler goroutine reclaimed
	case <-time.After(2 * time.Second):
		t.Fatal("serveLocalUpgrade leaked: hung local (no FIN) never reclaimed on tunnel close (#330-1)")
	}

	conn.mu.Lock()
	rx := string(conn.received)
	conn.mu.Unlock()
	if !strings.Contains(rx, "V") {
		t.Fatalf("pre-FIN visitor bytes must reach the local service, got %q", rx)
	}
}

// #337-2 (MED): an RST arriving while the local service is hung must still
// reclaim the handler - pump aborts the stream (ctx cancel + body pipe
// close), which fires the ctx.Done arm and actively closes rwc.
func TestServeLocalUpgradeRstArrivalReclaimsHungLocal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tun := &Tunnel{
		ctx:     ctx,
		cancel:  cancel,
		out:     make(chan wsOut, 8),
		closed:  make(chan struct{}),
		streams: make(map[uint32]*clientStream),
	}

	sctx, scancel := context.WithCancel(ctx)
	st := &clientStream{
		id:     2,
		t:      tun,
		ctx:    sctx,
		cancel: scancel,
		frames: make(chan protocol.Frame, 8),
		done:   make(chan struct{}),
	}
	tun.mu.Lock()
	tun.streams[2] = st
	tun.mu.Unlock()
	st.reqPr, st.reqPw = io.Pipe()

	conn := &hungLocalConn{closedCh: make(chan struct{})}
	resp := &http.Response{Header: http.Header{}, StatusCode: http.StatusSwitchingProtocols, Body: conn}

	served := make(chan struct{})
	go func() {
		defer close(served)
		tun.serveLocalUpgrade(st, resp)
	}()
	go st.pump()

	// visitor sends a byte, then the server RSTs the stream (visitor gave up)
	st.frames <- protocol.Frame{Kind: protocol.KindData, StreamID: 2, Payload: []byte("V")}
	st.frames <- protocol.Frame{Kind: protocol.KindRst, StreamID: 2}

	select {
	case <-served:
		// reclaimed via the ctx.Done arm: pump abort cancelled ctx, which
		// actively closed rwc, waking the parked read
	case <-time.After(2 * time.Second):
		t.Fatal("serveLocalUpgrade leaked: RST arrival with hung local never reclaimed (#337-2)")
	}

	conn.mu.Lock()
	rx := string(conn.received)
	conn.mu.Unlock()
	if !strings.Contains(rx, "V") {
		t.Fatalf("pre-RST visitor bytes must reach the local service, got %q", rx)
	}
}
