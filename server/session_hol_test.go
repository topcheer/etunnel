package server

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/topcheer/etunnel/protocol"
)

// Issue #8: a stream whose external consumer stopped reading must not be able
// to freeze the shared WebSocket reader (and with it every other stream plus
// ping/pong handling).

func newHOLSession(t *testing.T) *Session {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Session{
		ctx:     ctx,
		cancel:  cancel,
		out:     make(chan wsOut, 64),
		closed:  make(chan struct{}),
		streams: make(map[uint32]*Stream),
	}
}

func newHOLStream(t *testing.T, s *Session, id uint32) *Stream {
	t.Helper()
	ctx, cancel := context.WithCancel(s.ctx)
	pr, pw := io.Pipe()
	st := &Stream{
		id:     id,
		sess:   s,
		ctx:    ctx,
		cancel: cancel,
		frames: make(chan protocol.Frame, 16),
		bodyPr: pr,
		bodyPw: pw,
		done:   make(chan struct{}),
	}
	s.mu.Lock()
	s.streams[id] = st
	s.mu.Unlock()
	t.Cleanup(func() { _ = pr.Close(); _ = pw.Close() })
	return st
}

func holFrame(id uint32) protocol.Frame {
	return protocol.Frame{Kind: protocol.KindData, StreamID: id, Payload: []byte("payload")}
}

// fillQueue occupies every slot of the stream's frame queue, i.e. the pump is
// blocked writing to an external consumer that stopped reading.
func fillQueue(t *testing.T, st *Stream) {
	t.Helper()
	for i := 0; i < cap(st.frames); i++ {
		select {
		case st.frames <- holFrame(st.id):
		default:
			t.Fatalf("queue filled after %d frames", i)
		}
	}
}

// A stalled stream is reset on its own, and a healthy stream keeps flowing.
func TestStalledStreamIsResetWithoutBlockingOthers(t *testing.T) {
	s := newHOLSession(t)
	slow := newHOLStream(t, s, 1)    // consumer gone: queue full, pump blocked
	healthy := newHOLStream(t, s, 3) // consumer reading normally
	fillQueue(t, slow)

	const stall = 50 * time.Millisecond
	start := time.Now()
	if ok := s.routeFrame(holFrame(1), stall); ok {
		t.Fatal("frame accepted into a full queue")
	}
	if elapsed := time.Since(start); elapsed < stall {
		t.Fatalf("returned in %s, before the stall window (%s)", elapsed, stall)
	}
	if !slow.finished() {
		t.Fatal("stalled stream was not finished")
	}

	// The RST is what tells the client this stream is over.
	select {
	case m := <-s.out:
		if f, err := protocol.ReadFrame(bytes.NewReader(m.data)); err != nil || f.Kind != protocol.KindRst || f.StreamID != 1 {
			t.Fatalf("want RST for stream 1, got %v (err=%v)", f.Kind, err)
		}
	default:
		t.Fatal("no RST enqueued for the stalled stream")
	}

	// The healthy stream must be unaffected: accepted promptly, no reset.
	start = time.Now()
	if ok := s.routeFrame(holFrame(3), stall); !ok {
		t.Fatal("healthy stream rejected after a sibling stalled")
	}
	if elapsed := time.Since(start); elapsed >= stall {
		t.Fatalf("healthy stream waited %s (stall window %s)", elapsed, stall)
	}
	select {
	case f := <-healthy.frames:
		if f.StreamID != 3 {
			t.Fatalf("wrong stream delivered: %d", f.StreamID)
		}
	case <-time.After(time.Second):
		t.Fatal("healthy stream never received its frame")
	}
	if healthy.finished() {
		t.Fatal("healthy stream was reset")
	}
}

// Controlled falsification: the pre-fix handoff (unbounded blocking send) does
// stall, which is exactly what let one slow consumer kill the whole tunnel at
// PingTimeout. This test pins that behaviour down so the bug cannot silently
// come back.
func TestUnboundedHandoffStallsTheReader(t *testing.T) {
	s := newHOLSession(t)
	slow := newHOLStream(t, s, 1)
	fillQueue(t, slow)

	blocked := make(chan struct{})
	go func() {
		defer close(blocked)
		select { // pre-fix dispatchFrame body
		case slow.frames <- holFrame(1):
		case <-slow.done:
		}
	}()

	select {
	case <-blocked:
		t.Fatal("pre-fix handoff did not block on a full queue: the #8 bug is unreproducible")
	case <-time.After(150 * time.Millisecond):
		// expected: the shared reader would be stuck here
	}

	close(slow.done) // finishing the stream releases the blocked send
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("blocked handoff never released after the stream finished")
	}
}
