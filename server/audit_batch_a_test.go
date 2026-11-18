package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/topcheer/etunnel/protocol"
)

// Issue #188: a HEAD response finishes the stream at header time; the body
// pipe must be closed (EOF) so nothing parks in bodyPr.Read forever and a
// late FIN lands on a removed stream harmlessly.
func TestHeadFinishClosesBodyPipe(t *testing.T) {
	s := newHOLSession(t)
	st := newHOLStream(t, s, 7)

	res := &protocol.Res{Type: protocol.MsgRes, StreamID: 7, Status: http.StatusOK}
	srv := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "https://dev.t.test/x", nil)
	req.Host = "dev.t.test"
	srv.writeTunnelResponseHeaders(rec, req, res, st)

	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d", rec.Code)
	}
	if !st.finished() {
		t.Fatal("HEAD did not finish the stream")
	}

	// The reader must observe EOF promptly, not block.
	got := make(chan error, 1)
	go func() {
		buf := make([]byte, 8)
		_, err := st.bodyPr.Read(buf)
		got <- err
	}()
	select {
	case err := <-got:
		if err == nil || !strings.Contains(err.Error(), "EOF") {
			t.Fatalf("body reader woke with %v, want EOF", err)
		}
	case <-time.After(time.Second):
		t.Fatal("body reader still parked after finish(): #188 leak")
	}

	// A late FIN for the removed stream must be dropped, not deadlock.
	if ok := s.routeFrame(protocol.Frame{Kind: protocol.KindFin, StreamID: 7}, 0); ok {
		t.Fatal("late FIN accepted for a finished stream")
	}
}

// Issue #200: a tunnel client sending an out-of-range res.Status must get a
// 502, not a net/http panic that kills the visitor connection.
func TestInvalidTunnelStatusServes502(t *testing.T) {
	s := newHOLSession(t)
	st := newHOLStream(t, s, 9)
	res := &protocol.Res{Type: protocol.MsgRes, StreamID: 9, Status: 42}
	srv := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://dev.t.test/", nil)
	req.Host = "dev.t.test"

	srv.writeTunnelResponseHeaders(rec, req, res, st) // must not panic

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("invalid status 42 served as %d, want 502", rec.Code)
	}
}

// Issue #194: Host header values are visitor-controlled and land in the
// generated 502/404 pages; they must be HTML-escaped.
func TestHostInjectionEscapedInErrorPages(t *testing.T) {
	srv := &Server{}
	rec := httptest.NewRecorder()
	srv.badGateway(rec, "<script>alert(1)</script>:1234", "msg<b>")
	body := rec.Body.String()
	if strings.Contains(body, "<script>") || strings.Contains(body, "msg<b>") {
		t.Fatalf("502 page contains unescaped injection: %q", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("502 page lost the (escaped) host value")
	}

	rec2 := httptest.NewRecorder()
	_ = rec2
	req := httptest.NewRequest(http.MethodGet, "https://x.t.test/", nil)
	req.Host = "<svg onload=alert(1)>"
	page := notFoundHTML(Config{}, req)
	if strings.Contains(page, "<svg") {
		t.Fatalf("404 page contains unescaped host: %q", page)
	}
}

// Issue #187: inbound X-Etunnel-* headers are this server's namespace and
// must never survive into the request a tunnel policy or the local app sees.
func TestInboundEtunnelHeadersStripped(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://dev.t.test/hook", nil)
	req.Header.Set("X-Etunnel-Webhook", "verified: stripe")
	req.Header.Set("X-Etunnel-Other", "1")
	req.Header.Set("X-Etunnel-Trace", "spoof")
	req.Header.Set("X-Keep-Me", "yes")

	stripEtunnelHeaders(req.Header)

	for _, k := range []string{"X-Etunnel-Webhook", "X-Etunnel-Other", "X-Etunnel-Trace"} {
		if req.Header.Get(k) != "" {
			t.Fatalf("%s survived stripping", k)
		}
	}
	if req.Header.Get("X-Keep-Me") != "yes" {
		t.Fatal("unrelated header was stripped")
	}
}

// Issue #187 end-to-end policy shape: with a webhook rule configured and a
// visitor pre-setting the verified marker WITHOUT any signature header, the
// marker must be gone by the time the request would be forwarded (the policy
// falls through for unsigned traffic; the header itself must not survive).
func TestSpoofedVerifiedMarkerDroppedWithoutSignature(t *testing.T) {
	s := newHOLSession(t)
	_ = s
	req := httptest.NewRequest(http.MethodPost, "https://dev.t.test/hook", strings.NewReader("{}"))
	req.Header.Set("X-Etunnel-Webhook", "verified: stripe")

	// Production order: strip at serveTunnelRequest entry, then policy.
	stripEtunnelHeaders(req.Header)

	tp := &TunnelPolicy{Webhooks: []WebhookRule{{Provider: "stripe", Secret: "whsec_test"}}}
	rec := httptest.NewRecorder()
	if !tp.apply(rec, req, []byte("{}")) {
		t.Fatalf("unsigned traffic unexpectedly rejected: %d", rec.Code)
	}
	if v := req.Header.Get("X-Etunnel-Webhook"); v != "" {
		t.Fatalf("spoofed marker survived: %q", v)
	}
}
