package server

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/topcheer/etunnel/protocol"
)

// #322-1 (M1): an SDK FIN half closes the stream - the server must forward
// post-FIN local data to the visitor before closing its direction.
func TestServeUpgradeHalfCloseForwardsPostFinData(t *testing.T) {
	s := &Server{metrics: &metrics{}, cfg: Config{StreamIdleTimeout: 5 * time.Second}}
	sess := newHOLSession(t)
	st := newHOLStream(t, sess, 9)

	visitor, peer := net.Pipe()
	defer visitor.Close()
	defer peer.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		res := &protocol.Res{Type: protocol.MsgRes, StreamID: 9, Status: http.StatusSwitchingProtocols}
		s.serveUpgrade(&hijackRecorder{ResponseRecorder: httptest.NewRecorder(), conn: visitor},
			httptest.NewRequest(http.MethodGet, "https://dev.t.test/ws", nil), sess, st, res)
	}()

	// consume the replayed 101 head
	head := make([]byte, 0, 128)
	buf := make([]byte, 1)
	peer.SetReadDeadline(time.Now().Add(3 * time.Second))
	for !bytes.HasSuffix(head, []byte("\r\n\r\n")) {
		if _, err := peer.Read(buf); err != nil {
			t.Fatalf("reading 101 head: %v", err)
		}
		head = append(head, buf[0])
	}

	// SDK sends local data, then its FIN (half close). Everything sent
	// before the FIN must reach the visitor - frames are injected through
	// the session dispatcher (routeFrame -> st.frames -> pump -> bodyPr),
	// the same path a real SDK's DATA/FIN frames take.
	sess.routeFrame(protocol.Frame{Kind: protocol.KindData, StreamID: 9, Payload: []byte("V1V2")}, 0)
	go st.pump() // the HOL harness constructs the stream without a pump
	t.Logf("step: V1V2 routed, pump started")
	time.Sleep(50 * time.Millisecond)
	sess.routeFrame(protocol.Frame{Kind: protocol.KindData, StreamID: 9, Payload: []byte("LATE")}, 0)
	t.Logf("step: LATE routed")
	time.Sleep(50 * time.Millisecond)
	sess.routeFrame(protocol.Frame{Kind: protocol.KindFin, StreamID: 9}, 0)
	t.Logf("step: FIN routed")

	peer.SetReadDeadline(time.Now().Add(3 * time.Second))
	var got bytes.Buffer
	readBuf := make([]byte, 512)
	for !bytes.Contains(got.Bytes(), []byte("LATE")) {
		n, err := peer.Read(readBuf)
		if n > 0 {
			got.Write(readBuf[:n])
		}
		if err != nil {
			t.Fatalf("visitor truncated: %v; got %q (frames=%d finished=%v)", err, got.String(), len(st.frames), st.finished())
		}
		if got.Len() > 4096 {
			break
		}
	}
	if !bytes.Contains(got.Bytes(), []byte("V1V2")) {
		t.Fatalf("V1V2 missing: %q", got.String())
	}
}
