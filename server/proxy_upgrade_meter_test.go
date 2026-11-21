package server

// #294: (F1) the upgrade downlink meters and bills exactly like the uplink
// and streamTunnelBody - tier.Bandwidth applies to upgraded streams too;
// (F2) streamTunnelBody refreshes a per-write StreamIdleTimeout deadline so
// a stalled visitor cannot pin a stream indefinitely.

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/topcheer/etunnel/protocol"
)

// hijackRecorder is a ResponseWriter whose Hijack hands out conn, letting
// unit tests drive serveUpgrade without a real listener.
type hijackRecorder struct {
	*httptest.ResponseRecorder
	conn net.Conn
}

func (h *hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.conn, bufio.NewReadWriter(bufio.NewReader(h.conn), bufio.NewWriter(h.conn)), nil
}

// pipeResponseWriter lets ResponseController manage deadlines on a net.Pipe,
// standing in for the real visitor connection.
type pipeResponseWriter struct{ net.Conn }

func (p pipeResponseWriter) Header() http.Header { return http.Header{} }
func (p pipeResponseWriter) WriteHeader(int)     {}
func (p pipeResponseWriter) Flush()              {}

func TestUpgradeDownlinkMetersBytes(t *testing.T) {
	s := &Server{metrics: &metrics{}, cfg: Config{StreamIdleTimeout: 5 * time.Second}}
	sess := newHOLSession(t)
	st := newHOLStream(t, sess, 7)

	visitor, peer := net.Pipe()
	defer visitor.Close()
	defer peer.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		res := &protocol.Res{Type: protocol.MsgRes, StreamID: 7, Status: http.StatusSwitchingProtocols}
		s.serveUpgrade(&hijackRecorder{ResponseRecorder: httptest.NewRecorder(), conn: visitor},
			httptest.NewRequest(http.MethodGet, "https://dev.t.test/ws", nil),
			sess, st, res)
	}()

	// Read (and discard) the replayed 101 head.
	head := make([]byte, 0, 128)
	buf := make([]byte, 1)
	peer.SetReadDeadline(time.Now().Add(5 * time.Second))
	for !bytes.HasSuffix(head, []byte("\r\n\r\n")) {
		if _, err := peer.Read(buf); err != nil {
			t.Fatalf("reading 101 head: %v (head=%q)", err, head)
		}
		head = append(head, buf[0])
	}

	// Drive the downlink: local app bytes -> tunnel stream -> visitor.
	payload := bytes.Repeat([]byte("upgrade-downlink-#294-"), 16) // 352B
	if _, err := st.bodyPw.Write(payload); err != nil {
		t.Fatalf("feed downlink: %v", err)
	}
	_ = st.bodyPw.CloseWithError(io.EOF)

	got, err := io.ReadAll(peer)
	if err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("read downlink: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("downlink payload mismatch: got %d bytes, want %d", len(got), len(payload))
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serveUpgrade did not finish")
	}
	if got, want := s.metrics.bytesOut.Load(), int64(len(payload)); got != want {
		t.Fatalf("upgrade downlink bytesOut = %d, want %d (#294 F1)", got, want)
	}
}

func TestStreamBodyWriteDeadlineReclaimsStalledStream(t *testing.T) {
	s := &Server{metrics: &metrics{}, cfg: Config{StreamIdleTimeout: 100 * time.Millisecond}}
	sess := newHOLSession(t)
	st := newHOLStream(t, sess, 5)

	visitor, peer := net.Pipe()
	defer visitor.Close()
	defer peer.Close()
	rw := pipeResponseWriter{Conn: visitor}
	_ = peer // nobody reads: the visitor TCP window is "full"

	// Keep the body pipe fed; the visitor never consumes.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		chunk := bytes.Repeat([]byte{0xAB}, 32*1024)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := st.bodyPw.Write(chunk); err != nil {
				return
			}
		}
	}()

	start := time.Now()
	finished := make(chan any, 1)
	go func() {
		defer func() { finished <- recover() }()
		s.streamTunnelBody(rw, httptest.NewRequest(http.MethodGet, "https://dev.t.test/x", nil),
			sess, st, &protocol.Res{Type: protocol.MsgRes, StreamID: 5, Status: 200})
		finished <- nil
	}()

	select {
	case p := <-finished:
		if p == nil {
			t.Fatal("stalled stream was not aborted (#294 F2)")
		}
		if p != http.ErrAbortHandler {
			t.Fatalf("abort panic = %v, want http.ErrAbortHandler", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("slow consumer still pinned the stream after 5s (deadline %v) (#294 F2)", s.cfg.StreamIdleTimeout)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("reclaim took %v, want ~%v", elapsed, s.cfg.StreamIdleTimeout)
	}
}
