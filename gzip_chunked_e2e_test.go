package etunnel_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/topcheer/etunnel/protocol"
)

// gzipChunkedBackend serves a fixed gzip entity the way NanoHTTPD-class
// local apps do for gzip-negotiating visitors: Content-Encoding: gzip, no
// Content-Length, body written in small flushed chunks (forced chunked
// transfer framing on the local hop).
func gzipChunkedBackend(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		for i := 0; i < len(payload); i += 100 {
			end := min(i+100, len(payload))
			if _, err := w.Write(payload[i:end]); err != nil {
				return
			}
			w.(http.Flusher).Flush() // forces Transfer-Encoding: chunked
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func gzipped(t *testing.T, original string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(original)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestGzipChunkedBodyStaysIdentity pins the #286 wire contract end to end:
// a local backend answering gzip with chunked transfer framing must reach
// the visitor as the pure gzip entity. The SDK replay de-frames the local
// transport bytes, the server strips the hop TE declaration and declares
// its own outbound framing, so a `curl --compressed`-class visitor gunzips
// exactly the original bytes (#286: burrow saw "a\r\n..." chunk-size lines
// in front of the gzip stream - browser content decode failure).
func TestGzipChunkedBodyStaysIdentity(t *testing.T) {
	original := strings.Repeat("etunnel #286 identity-body payload - ", 42) // ~1.6KB, burrow-scale
	payload := gzipped(t, original)

	local := gzipChunkedBackend(t, payload)
	_, srvURL := startTestServer(t, nil)
	tun := connectTunnel(t, srvURL, local.URL, nil)

	cli := clientThroughServer(t, srvURL)
	req, err := http.NewRequest(http.MethodGet, tun.URL()+"/index.html", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Setting Accept-Encoding manually (like a real browser) keeps the Go
	// transport from auto-decompressing: we get the raw wire entity back.
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatalf("visitor request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ce := resp.Header.Get("Content-Encoding"); ce != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", ce)
	}
	// The server's own outbound hop declares the framing it applies (h1:
	// chunked). Undeclared framing is exactly what #286 forbids.
	if len(resp.TransferEncoding) != 1 || resp.TransferEncoding[0] != "chunked" {
		t.Fatalf("TransferEncoding = %v, want [chunked]", resp.TransferEncoding)
	}
	// hop-by-hop hygiene: the local chunked declaration must not leak
	// through as a visitor-visible header, and neither may a stale CL.
	if te := resp.Header.Get("Transfer-Encoding"); te != "" {
		t.Fatalf("Transfer-Encoding leaked through as header: %q", te)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		t.Fatalf("stale Content-Length leaked through: %q", cl)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	// The decisive #286 assertion: the entity is byte-for-byte the gzip
	// stream the backend produced - no chunk-size lines in front of it. If
	// framing ever leaked, the gunzip diagnostic below names it.
	if !bytes.Equal(body, payload) {
		zr, zerr := gzip.NewReader(bytes.NewReader(body))
		if zerr != nil {
			t.Fatalf("body is not a pure gzip stream (first bytes %x): %v", body[:min(8, len(body))], zerr)
		}
		got, gerr := io.ReadAll(zr)
		t.Fatalf("body != backend gzip entity: len=%d want=%d gunzip err=%v got=%d bytes",
			len(body), len(payload), gerr, len(got))
	}
}

// TestFrameLeakSniffWarns documents the broken-client path (#286): a tunnel
// client that replays the local service's transfer-framed bytes verbatim -
// exactly the raw-socket replay stack class the burrow report implicates -
// is relayed untouched (the proxy must not second-guess payload bytes) but
// leaves a pointed server-side trace naming the contract violation, instead
// of surfacing only as a mysterious visitor-side gunzip failure.
func TestFrameLeakSniffWarns(t *testing.T) {
	_, srvURL := startTestServer(t, nil)
	wsURL := "ws" + strings.TrimPrefix(srvURL, "http") + "/tunnel"

	// Raw tunnel client: a hostile/broken replay stack speaking the wire
	// protocol directly, answering every req with chunk-size framed bytes
	// inside DATA payloads and headers that declare no framing.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial tunnel: %v", err)
	}
	t.Cleanup(func() { ws.Close(websocket.StatusNormalClosure, "") })

	hello, _ := json.Marshal(protocol.Hello{Type: "hello", Proto: 1})
	if err := ws.Write(ctx, websocket.MessageText, hello); err != nil {
		t.Fatalf("hello: %v", err)
	}
	_, rd, err := ws.Reader(ctx)
	if err != nil {
		t.Fatalf("welcome read: %v", err)
	}
	msg, err := io.ReadAll(rd)
	if err != nil {
		t.Fatalf("welcome: %v", err)
	}
	var welcome protocol.Welcome
	if err := json.Unmarshal(msg, &welcome); err != nil {
		t.Fatalf("welcome parse: %v", err)
	}

	go func() {
		for {
			_, rd, err := ws.Reader(ctx)
			if err != nil {
				return
			}
			raw, err := io.ReadAll(rd)
			if err != nil {
				return
			}
			var req protocol.Req
			if json.Unmarshal(raw, &req) != nil || req.Type != "req" {
				continue
			}
			res, _ := json.Marshal(protocol.Res{
				Type:     "res",
				StreamID: req.StreamID,
				Status:   200,
				Headers:  map[string][]string{"Content-Encoding": {"gzip"}},
			})
			_ = ws.Write(ctx, websocket.MessageText, res)
			// The leak: local chunked framing shipped verbatim, undeclared.
			leak := protocol.Frame{
				Kind:     protocol.KindData,
				StreamID: req.StreamID,
				Payload:  []byte("a\r\n200\r\n...gzip payload would follow...\r\n0\r\n\r\n"),
			}
			_ = ws.Write(ctx, websocket.MessageBinary, leak.Encode())
			_ = ws.Write(ctx, websocket.MessageBinary,
				protocol.Frame{Kind: protocol.KindFin, StreamID: req.StreamID}.Encode())
			return
		}
	}()

	cli := clientThroughServer(t, srvURL)
	req, err := http.NewRequest(http.MethodGet, strings.TrimSuffix(welcome.URL, "/")+"/index.html", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Manual Accept-Encoding (browser-style) keeps the Go transport from
	// auto-decompressing: the raw wire entity must come back untouched.
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatalf("visitor request: %v", err)
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	// Relayed verbatim: the server is a dumb pipe for payload bytes (the
	// companion sniff warning is asserted in server/proxy_frame_sniff_test.go).
	if !bytes.Contains(body, []byte("a\r\n200\r\n")) {
		t.Fatalf("framed bytes not relayed verbatim: status=%d readErr=%v hdr=%v body=%q", resp.StatusCode, readErr, resp.Header, body)
	}
}
