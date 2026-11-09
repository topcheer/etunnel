package etunnel

import (
	"bytes"
	"context"
	"testing"

	"github.com/topcheer/etunnel/protocol"
)

func newRegisterTestTunnel(t *testing.T) *Tunnel {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Tunnel{
		ctx:     ctx,
		cancel:  cancel,
		out:     make(chan wsOut, 64),
		closed:  make(chan struct{}),
		streams: make(map[uint32]*clientStream),
	}
}

func expectRst(t *testing.T, tun *Tunnel, id uint32) {
	t.Helper()
	select {
	case m := <-tun.out:
		f, err := protocol.ReadFrame(bytes.NewReader(m.data))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if f.Kind != protocol.KindRst || f.StreamID != id {
			t.Fatalf("want RST for %d, got kind=%v id=%d", id, f.Kind, f.StreamID)
		}
	default:
		t.Fatalf("no RST sent back for refused stream %d", id)
	}
}

// Issue #11: a repeated stream id must not orphan the live stream; the server
// is told the replay is refused.
func TestRegisterClientStreamRefusesDuplicateID(t *testing.T) {
	tun := newRegisterTestTunnel(t)

	first := tun.registerClientStream(5)
	if first == nil {
		t.Fatal("first registration failed")
	}
	defer first.abort()

	if again := tun.registerClientStream(5); again != nil {
		t.Fatal("duplicate stream id was accepted")
	}
	if tun.streams[5] != first {
		t.Fatal("live stream was replaced by the duplicate")
	}
	expectRst(t, tun, 5)
}

// Issue #11: a hostile server must not be able to grow client-side streams
// (goroutines, pipes, queues) without bound.
func TestRegisterClientStreamEnforcesLimit(t *testing.T) {
	tun := newRegisterTestTunnel(t)

	var opened []*clientStream
	for i := 0; i < maxClientStreams; i++ {
		st := tun.registerClientStream(uint32(2*i + 1))
		if st == nil {
			t.Fatalf("registration %d refused below the limit", i)
		}
		opened = append(opened, st)
	}
	defer func() {
		for _, st := range opened {
			st.abort()
		}
	}()

	over := tun.registerClientStream(uint32(2*maxClientStreams + 1))
	if over != nil {
		t.Fatal("registration above the limit was accepted")
	}
	if len(tun.streams) != maxClientStreams {
		t.Fatalf("stream count = %d, want %d", len(tun.streams), maxClientStreams)
	}
	expectRst(t, tun, uint32(2*maxClientStreams+1))
}
