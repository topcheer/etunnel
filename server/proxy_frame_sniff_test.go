package server

// #286 diagnostics: the tunnel wire contract (PROTOCOL.md §2) is that res
// DATA frames carry the identity entity body. When a replay stack leaks the
// local service's transfer-framed bytes onto the wire (raw-socket replay
// class, seen against burrow 2026-11-21), the proxy relays the payload
// untouched but logs a pointed warning instead of leaving the failure as a
// mysterious visitor-side gunzip error.

import (
	"bytes"
	"fmt"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/topcheer/etunnel/protocol"
)

func runStreamBody(t *testing.T, res *protocol.Res, body []byte) string {
	t.Helper()
	s := newHOLSession(t)
	st := newHOLStream(t, s, 11)

	var logged bytes.Buffer
	orig := logPrintf
	logPrintf = func(f string, a ...any) (int, error) { return fmt.Fprintf(&logged, f, a...) }
	t.Cleanup(func() { logPrintf = orig })

	go func() {
		if len(body) > 0 {
			_, _ = st.bodyPw.Write(body)
		}
		_ = st.bodyPw.CloseWithError(io.EOF)
	}()

	srv := &Server{metrics: &metrics{}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "https://dev.t.test/index.html", nil)
	req.Host = "dev.t.test"
	srv.streamTunnelBody(rec, req, s, st, res)
	return logged.String()
}

func TestFrameSniffWarnsOnUndeclaredFraming(t *testing.T) {
	res := &protocol.Res{
		Type: protocol.MsgRes, StreamID: 11, Status: 200,
		Headers: map[string][]string{"Content-Encoding": {"gzip"}},
	}
	// no Content-Length, no Transfer-Encoding, chunk-size opening line
	got := runStreamBody(t, res, []byte("a\r\n200\r\n...gzip payload would follow...\r\n0\r\n\r\n"))
	if !bytes.Contains([]byte(got), []byte("chunk-size framing")) {
		t.Fatalf("no sniff warning for leaked framing: %q", got)
	}
}

func TestFrameSniffQuietForIdentityBody(t *testing.T) {
	t.Run("gzip", func(t *testing.T) {
		res := &protocol.Res{
			Type: protocol.MsgRes, StreamID: 11, Status: 200,
			Headers: map[string][]string{
				"Content-Encoding": {"gzip"},
				"Content-Length":   {"5"}, // declared length suppresses sniffing
			},
		}
		// gzip magic 1f 8b ... never looks like a chunk-size line, but the
		// declared CL must short-circuit the sniff regardless.
		if got := runStreamBody(t, res, []byte("\x1f\x8b\x08\x00a")); got != "" {
			t.Fatalf("sniff fired for declared gzip body: %q", got)
		}
	})
	t.Run("identityNoCL", func(t *testing.T) {
		res := &protocol.Res{Type: protocol.MsgRes, StreamID: 11, Status: 200}
		// An identity body that opens with hex digits but has no CRLF after
		// them must not warn (hex dump files, binary blobs).
		if got := runStreamBody(t, res, []byte("\x1f\x8b\x08\x00rest")); got != "" {
			t.Fatalf("sniff fired for identity gzip body: %q", got)
		}
		if got := runStreamBody(t, res, []byte("deadbeef no newline")); got != "" {
			t.Fatalf("sniff fired for hex-like identity body: %q", got)
		}
	})
}
