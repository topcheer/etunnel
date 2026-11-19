package server

// Regression test for #217: pumpRequestBody must have a real cancellation
// path. Once the stream is finished (HEAD answers finish at header time and
// abandon the request body), the pump may not stay parked in r.Body.Read -
// that leaked a goroutine + fd + tunnel bandwidth per request, uncounted by
// MaxStreamsPerTunnel because the stream was already removed. The normal
// drain-to-EOF path is exercised end-to-end by the root integration tests.

import (
	"io"
	"net/http"
	"testing"
	"time"
)

// blockingBody never yields data; Read parks until Close.
type blockingBody struct{ unblock chan struct{} }

func (b *blockingBody) Read(p []byte) (int, error) {
	<-b.unblock
	return 0, io.ErrClosedPipe
}

func (b *blockingBody) Close() error {
	select {
	case <-b.unblock:
	default:
		close(b.unblock)
	}
	return nil
}

// The loop-head finished() guard must release the pump even though the body
// reader would never return from Read.
func TestPumpRequestBodyStopsAfterStreamFinished(t *testing.T) {
	s := &Server{}
	sess := &Session{}
	done := make(chan struct{})
	close(done) // stream already finished (HEAD path)
	st := &Stream{id: 1, sess: sess, done: done}

	body := &blockingBody{unblock: make(chan struct{})}
	defer body.Close()

	gone := make(chan struct{})
	go func() {
		s.pumpRequestBody(sess, st, &http.Request{Body: io.NopCloser(body)})
		close(gone)
	}()
	select {
	case <-gone:
		// pump exited without consuming anything - as intended
	case <-time.After(2 * time.Second):
		t.Fatal("pump stayed parked after the stream finished (#217 regression)")
	}
}
