package etunnel_test

// Regression test for #230: the client's readLoop is the tunnel's only
// socket reader, and it used to deliver frames with a BLOCKING send into
// each stream's 16-slot queue. One stream whose local consumer never read
// its request body parked the whole tunnel (all streams, ping/pong), until
// the ping watchdog tore it down. The fix resets the stalled stream
// instead - the client-side counterpart of the server's
// TestStalledStreamIsResetWithoutBlockingOthers.

import (
	"io"
	"net/http"
	"testing"
	"time"
)

// zeroReader yields an endless stream of 'x' without allocation, so the
// stalled POST can push far past every buffer in the path (loopback TCP
// alone can absorb several MiB).
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestClientStalledStreamDoesNotBlockTunnel(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/stall", func(w http.ResponseWriter, r *http.Request) {
		// Local consumer that never reads the request body: the SDK's
		// per-stream queue (16 x 32 KiB) fills and the stream stalls. Bounds
		// itself by the request context, so the test server's cleanup never
		// waits out the full park when the stalled exchange is reset.
		select {
		case <-r.Context().Done():
		case <-time.After(60 * time.Second):
		}
	})
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	})
	local := newLocalService(t, mux)
	_, srvURL := startTestServer(t, nil)
	tun := connectTunnel(t, srvURL, local, nil)
	cli := clientThroughServer(t, srvURL)

	// Start the stalled exchange: push far past the frames watermark
	// (and past loopback TCP buffering) while the consumer refuses to read.
	stallDone := make(chan error, 1)
	stallStatus := 0
	go func() {
		resp, err := cli.Post(tun.URL()+"/stall", "text/plain",
			io.LimitReader(zeroReader{}, 64<<20))
		if resp != nil {
			stallStatus = resp.StatusCode
			resp.Body.Close()
		}
		stallDone <- err
	}()

	// Other streams on the same tunnel must keep working while one is
	// stalled; with the old blocking send this failed almost immediately
	// at loopback speed.
	for i := 0; i < 20; i++ {
		resp, err := cli.Get(tun.URL() + "/ok")
		if err != nil {
			t.Fatalf("/ok request %d failed while another stream was stalled: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != "ok" {
			t.Fatalf("/ok body %q", body)
		}
	}

	// The stalled stream must be RESET quickly - the POST concludes with a
	// transport error (hard reset) or a non-200 response (server-side reset
	// of the exchange) well before the parked handler's own 60s bound -
	// rather than holding the tunnel hostage until a ping timeout.
	select {
	case err := <-stallDone:
		if err == nil && stallStatus == 200 {
			t.Fatalf("stalled upload completed with 200; expected a reset (status %d)", stallStatus)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("stalled POST never returned: stream was not reset (#230 regression)")
	}

	// The tunnel itself must still be alive and serving.
	resp, err := cli.Get(tun.URL() + "/ok")
	if err != nil {
		t.Fatalf("tunnel unhealthy after stream reset: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "ok" {
		t.Fatalf("post-reset /ok body %q", body)
	}
}
