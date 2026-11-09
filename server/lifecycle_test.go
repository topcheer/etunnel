package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/topcheer/etunnel/protocol"
)

func newLifecycleSession(t *testing.T, outCap int) *Session {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Session{
		id:      "test-session",
		srv:     &Server{cfg: Config{}, reg: &Registry{subs: map[string]*Session{}}},
		ctx:     ctx,
		cancel:  cancel,
		out:     make(chan wsOut, outCap),
		closed:  make(chan struct{}),
		streams: make(map[uint32]*Stream),
	}
}

// #12: an early close must not leave the TTL timer holding the session until
// expiry (an hour to 30 days later).
func TestCloseStopsTTLTimer(t *testing.T) {
	s := newLifecycleSession(t, 4)
	var fired atomic.Bool
	s.ttlTimer = time.AfterFunc(60*time.Millisecond, func() { fired.Store(true) })

	s.Close("client bye")

	time.Sleep(150 * time.Millisecond)
	if fired.Load() {
		t.Fatal("TTL timer fired after the session was closed early")
	}
}

// #12: Close must stay prompt when the outbound queue is already full, while
// still trying to deliver the bye.
func TestCloseIsBoundedWithFullQueue(t *testing.T) {
	s := newLifecycleSession(t, 1)
	s.out <- wsOut{text: false, data: []byte("occupied")} // queue is now full

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Close(protocol.ErrCodeTTlExpired)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked on a full outbound queue")
	}
	select {
	case <-s.closed:
	default:
		t.Fatal("closed signal not raised")
	}
	// The reason must survive a full queue: it is parked for the writer's drain
	// path instead of being dropped (a dropped bye leaves the peer unable to
	// tell why it was disconnected).
	if v, ok := s.byePending.Load().([]byte); !ok || len(v) == 0 {
		t.Fatal("close reason was dropped instead of handed to the writer")
	}
}

// #12: the crypto success poll must stop as soon as the client goes away.
func TestCryptoSuccessRespectsRequestContext(t *testing.T) {
	s := newBillingTestServer(t)
	s.billing.NowPaymentsAPIKey = "test-key" // configured, so the poll path is taken

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // client already gone
	req := httptest.NewRequest(http.MethodGet, "/billing/crypto/success?payment=123", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	start := time.Now()
	s.handleCryptoSuccess(rec, req)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("handler kept polling for %s after the request was cancelled", elapsed)
	}
}
