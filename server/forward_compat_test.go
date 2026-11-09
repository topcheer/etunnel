package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/topcheer/etunnel/protocol"
)

// Issue #14: a newer peer's unknown control message must not kill the session.

func TestUnknownControlTypeIsIgnored(t *testing.T) {
	s := &Session{streams: make(map[uint32]*Stream)}
	if err := s.handleControl([]byte(`{"type":"future_thing","v":2}`)); err != nil {
		t.Fatalf("unknown control type closed the session: %v", err)
	}
	// A malformed *known* type is still fatal: that peer is broken, not newer.
	if err := s.handleControl([]byte(`{"type":"pong","ts":"nope"}`)); err == nil {
		t.Fatal("malformed known control type was accepted")
	}
}

// Issue #14: ErrCodeTooManyStreams was defined but never sent, so SDKs saw only
// the browser-facing 429 with no reason.
func TestStreamLimitReportsTooManyStreams(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sess := &Session{
		id:      "limit-session",
		srv:     &Server{cfg: Config{MaxStreamsPerTunnel: 1}},
		ctx:     ctx,
		cancel:  cancel,
		out:     make(chan wsOut, 8),
		closed:  make(chan struct{}),
		streams: map[uint32]*Stream{1: {id: 1}},
	}
	srv := sess.srv

	rec := httptest.NewRecorder()
	srv.serveTunnelRequest(rec, httptest.NewRequest(http.MethodGet, "http://dev.t.test/", nil), sess)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	select {
	case m := <-sess.out:
		var e protocol.ErrorMsg
		if err := json.Unmarshal(m.data, &e); err != nil {
			t.Fatalf("decode error message: %v", err)
		}
		if e.Code != protocol.ErrCodeTooManyStreams {
			t.Fatalf("code = %q, want %q", e.Code, protocol.ErrCodeTooManyStreams)
		}
	default:
		t.Fatal("no error message reached the client")
	}
}
