package server

import (
	"context"
	"io"
	"testing"

	"github.com/topcheer/etunnel/protocol"
)

func newStreamIDTestSession(t *testing.T) *Session {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Session{
		id:      "stream-id-session",
		srv:     &Server{cfg: Config{MaxStreamsPerTunnel: 256}},
		ctx:     ctx,
		cancel:  cancel,
		out:     make(chan wsOut, 8),
		closed:  make(chan struct{}),
		streams: make(map[uint32]*Stream),
	}
}

// stubStream builds a stream shape sufficient for allocation tests and
// registers it, so len(s.streams) reflects reality.
func stubStream(t *testing.T, s *Session, id uint32) *Stream {
	t.Helper()
	ctx, cancel := context.WithCancel(s.ctx)
	pr, pw := io.Pipe()
	st := &Stream{
		id:     id,
		sess:   s,
		ctx:    ctx,
		cancel: cancel,
		frames: make(chan protocol.Frame, 1),
		bodyPr: pr,
		bodyPw: pw,
		done:   make(chan struct{}),
	}
	s.streams[id] = st
	t.Cleanup(func() { _ = pr.Close(); _ = pw.Close() })
	return st
}

// Issue #11: after the odd-ID counter wraps, allocation must skip IDs that are
// still in use instead of overwriting a live stream.
func TestOpenStreamSkipsLiveIDOnWrap(t *testing.T) {
	s := newStreamIDTestSession(t)

	live := stubStream(t, s, 1)
	s.nextID = 1 // as if the counter wrapped back onto a live stream

	st := s.OpenStream()
	if st == nil {
		t.Fatal("OpenStream refused instead of skipping the live id")
	}
	defer st.abort()
	if st.id == 1 {
		t.Fatal("allocated the live stream's id: its frames and pump would be clobbered")
	}
	if s.streams[1] != live {
		t.Fatal("live stream was replaced")
	}
	if s.streams[st.id] != st {
		t.Fatal("new stream not registered")
	}
}

// Without a collision the allocation is unchanged.
func TestOpenStreamAllocatesNextID(t *testing.T) {
	s := newStreamIDTestSession(t)
	st := s.OpenStream()
	if st == nil {
		t.Fatal("OpenStream failed")
	}
	defer st.abort()
	if st.id != 1 { // nextID starts at zero -> +2 => odd ids from 1
		t.Fatalf("first id = %d, want 1", st.id)
	}
	if s.nextID != 3 {
		t.Fatalf("nextID = %d, want 3", s.nextID)
	}
}
