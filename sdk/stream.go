package etunnel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/topcheer/etunnel/protocol"
)

var errStreamReset = errors.New("etunnel: stream reset by peer")

// clientStream is one HTTP exchange initiated by the server via a Req meta
// message. DATA/FIN frames from the server feed the local request body pipe.
type clientStream struct {
	id uint32
	t  *Tunnel

	ctx    context.Context
	cancel context.CancelFunc

	frames chan protocol.Frame
	reqPr  *io.PipeReader
	reqPw  *io.PipeWriter

	done     chan struct{}
	doneOnce sync.Once
}

func (st *clientStream) finished() bool {
	select {
	case <-st.done:
		return true
	default:
		return false
	}
}

func (st *clientStream) finish() {
	st.doneOnce.Do(func() {
		close(st.done)
		st.t.mu.Lock()
		delete(st.t.streams, st.id)
		st.t.mu.Unlock()
	})
}

func (st *clientStream) abort() {
	st.finish()
	st.cancel()
	_ = st.reqPw.CloseWithError(errStreamReset)
}

// pump feeds incoming request-body frames into the local request body pipe.
func (st *clientStream) pump() {
	for {
		select {
		case f := <-st.frames:
			switch f.Kind {
			case protocol.KindData:
				if _, err := st.reqPw.Write(f.Payload); err != nil {
					st.finish()
					return
				}
			case protocol.KindFin:
				_ = st.reqPw.CloseWithError(io.EOF)
				return
			case protocol.KindRst:
				_ = st.reqPw.CloseWithError(errStreamReset)
				st.finish()
				return
			}
		case <-st.done:
			return
		}
	}
}

func isHop(k string) bool {
	switch strings.ToLower(k) {
	case "connection", "proxy-connection", "keep-alive", "proxy-authenticate",
		"proxy-authorization", "te", "trailer", "transfer-encoding":
		return true
	}
	return false
}

func hasUpgradeHeader(h http.Header) bool {
	for _, v := range h.Values("Connection") {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
				return true
			}
		}
	}
	return false
}

func mapToHeader(m map[string][]string) http.Header {
	h := make(http.Header, len(m))
	for k, vs := range m {
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	return h
}

// maxClientStreams bounds concurrently open streams driven by the server. It
// matches the server's default per-tunnel limit; a hostile or buggy server
// must not be able to exhaust client memory, file descriptors and goroutines
// by opening streams without end (#11).
const maxClientStreams = 256

// registerClientStream creates and registers the stream for an incoming Req.
// Must run on the reader goroutine before any stream frames are dispatched.
// It returns nil when the stream is refused (session closing, the server
// replayed a live id, or the limit is reached) and tells the server so with an
// RST.
func (t *Tunnel) registerClientStream(id uint32) *clientStream {
	t.mu.Lock()
	select {
	case <-t.closed:
		t.mu.Unlock()
		return nil
	default:
	}
	if _, dup := t.streams[id]; dup {
		// A well-behaved server never reuses a live id; keep the existing
		// stream instead of silently orphaning it (and its goroutines).
		t.mu.Unlock()
		t.logf("etunnel: server reused live stream id %d; refusing it", id)
		_ = t.sendRst(id)
		return nil
	}
	if len(t.streams) >= maxClientStreams {
		t.mu.Unlock()
		t.logf("etunnel: refusing stream %d: %d concurrent streams is the limit", id, maxClientStreams)
		_ = t.sendRst(id)
		return nil
	}

	ctx, cancel := context.WithCancel(t.ctx)
	pr, pw := io.Pipe()
	st := &clientStream{
		id:     id,
		t:      t,
		ctx:    ctx,
		cancel: cancel,
		frames: make(chan protocol.Frame, 16),
		reqPr:  pr,
		reqPw:  pw,
		done:   make(chan struct{}),
	}
	t.streams[id] = st
	t.mu.Unlock()
	go st.pump()
	return st
}

// serveStream executes one request against the local service and streams the
// response back over the tunnel. The stream must already be registered.
func (t *Tunnel) serveStream(st *clientStream, req *protocol.Req) {

	defer func() {
		if !st.finished() {
			_ = t.sendRst(st.id)
			st.abort()
		}
	}()

	path := req.Path
	if path == "" || path[0] != '/' {
		path = "/" + path
	}
	hdrs := mapToHeader(req.Headers)
	isUpgrade := hasUpgradeHeader(hdrs)

	// Upgrade handshakes carry no body; the raw tunnel traffic that follows
	// flows through the stream frames (pump -> reqPr pipe).
	var body io.Reader = st.reqPr
	if isUpgrade {
		body = http.NoBody
	}
	hreq, err := http.NewRequestWithContext(st.ctx, req.Method, "http://"+t.localAddr+path, body)
	if err != nil {
		t.sendLocalError(st, "bad request path")
		return
	}
	for k, vs := range hdrs {
		if !isUpgrade && isHop(k) {
			continue
		}
		for _, v := range vs {
			hreq.Header.Add(k, v)
		}
	}
	if cl := hdrs.Get("Content-Length"); cl != "" {
		if n, err := strconv.ParseInt(cl, 10, 64); err == nil {
			hreq.ContentLength = n
			hreq.Header.Del("Content-Length")
		}
	}
	// Restore the original public host (X-Forwarded-Host from the server):
	// req.Host sets the Host header the local service sees, so absolute
	// URLs and Origin/Host same-origin checks work through the tunnel.
	if fh := hdrs.Values("X-Forwarded-Host"); len(fh) > 0 && fh[0] != "" {
		hreq.Host = fh[0]
	}

	var resp *http.Response
	if isUpgrade {
		// Raw transport round trip keeps 101 upgrade responses passable.
		resp, err = t.transport.RoundTrip(hreq)
	} else {
		resp, err = t.httpClient.Do(hreq)
	}
	if err != nil {
		t.sendLocalError(st, err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusSwitchingProtocols {
		t.serveLocalUpgrade(st, resp)
		return
	}

	// Response meta.
	resHdr := make(map[string][]string, len(resp.Header))
	for k, vs := range resp.Header {
		if isHop(k) || strings.EqualFold(k, "Upgrade") {
			continue
		}
		resHdr[http.CanonicalHeaderKey(k)] = vs
	}
	if err := t.sendJSON(&protocol.Res{
		Type:     protocol.MsgRes,
		StreamID: st.id,
		Status:   resp.StatusCode,
		Headers:  resHdr,
	}); err != nil {
		st.abort()
		return
	}

	buf := make([]byte, protocol.MaxPayload)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if serr := t.sendData(st.id, buf[:n]); serr != nil {
				st.abort()
				return
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				_ = t.sendFin(st.id)
				st.finish()
			} else {
				_ = t.sendRst(st.id)
				st.finish()
			}
			return
		}
	}
}

// sendLocalError reports a local-service failure as a 502 response.
func (t *Tunnel) sendLocalError(st *clientStream, msg string) {
	body := localErrorPage(msg)
	_ = t.sendJSON(&protocol.Res{
		Type:     protocol.MsgRes,
		StreamID: st.id,
		Status:   http.StatusBadGateway,
		Headers: map[string][]string{
			"Content-Type": {"text/plain; charset=utf-8"},
		},
	})
	_ = t.sendData(st.id, []byte(body))
	_ = t.sendFin(st.id)
	st.finish()
}

// serveLocalUpgrade relays raw bytes between the tunnel stream and the
// upgraded local connection (resp.Body is an io.ReadWriteCloser here).
func (t *Tunnel) serveLocalUpgrade(st *clientStream, resp *http.Response) {
	resHdr := make(map[string][]string, len(resp.Header))
	for k, vs := range resp.Header {
		resHdr[http.CanonicalHeaderKey(k)] = vs
	}
	if err := t.sendJSON(&protocol.Res{
		Type:     protocol.MsgRes,
		StreamID: st.id,
		Status:   http.StatusSwitchingProtocols,
		Headers:  resHdr,
	}); err != nil {
		st.abort()
		return
	}
	rwc, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		st.abort()
		return
	}

	done := make(chan struct{}, 2)

	// external -> local (frames arriving from server are the request side).
	go func() {
		defer func() { done <- struct{}{} }()
		_, _ = io.Copy(rwc, st.reqPr)
		_ = rwc.Close()
	}()

	// local -> external.
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, protocol.MaxPayload)
		for {
			n, rerr := rwc.Read(buf)
			if n > 0 {
				if serr := t.sendData(st.id, buf[:n]); serr != nil {
					_ = rwc.Close()
					return
				}
			}
			if rerr != nil {
				_ = t.sendFin(st.id)
				return
			}
		}
	}()

	<-done
	st.abort()
	<-done
}

// localErrorPage renders the geek-chic 502 page served when the local app is
// down. Kept in the SDK (not the server) because this is what the tunnel's
// visitor actually sees - the bytes travel through the server untouched.
func localErrorPage(msg string) string {
	msg = strings.ReplaceAll(msg, "&", "&amp;")
	msg = strings.ReplaceAll(msg, "<", "&lt;")
	msg = strings.ReplaceAll(msg, ">", "&gt;")
	return `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>502 - local app down</title>
<style>
:root{--bg:#0b0e14;--panel:#0d1117;--line:#21262d;--fg:#e6edf3;--dim:#8b949e;--green:#3fb950}
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:ui-monospace,'SF Mono',Menlo,Consolas,monospace;background:var(--bg);color:var(--fg);
display:flex;min-height:100vh;align-items:center;justify-content:center;line-height:1.6}
main{max-width:600px;padding:2.5rem 1.5rem;width:100%}
.badge{display:inline-block;border:1px solid rgba(248,81,73,.4);color:#f85149;border-radius:99px;
padding:.2rem .8rem;font-size:.72rem;letter-spacing:.08em;text-transform:uppercase;margin-bottom:1.1rem}
h1{font-size:1.6rem;letter-spacing:-.01em;margin-bottom:.4rem}
h1 .g{color:var(--green)}
p{color:var(--dim);font-size:.9rem;margin-bottom:1.4rem}
.panel{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:1.1rem 1.3rem;margin-bottom:1.1rem}
.panel .k{color:var(--dim);font-size:.72rem;text-transform:uppercase;letter-spacing:.08em;margin-bottom:.3rem}
.panel .v{font-size:.9rem;word-break:break-all}
.err{color:#f85149}
.tip{border-left:3px solid var(--green);padding:.8rem 1rem;margin-top:1.2rem;font-size:.82rem;color:var(--dim)}
.tip b{color:var(--fg)}
footer{margin-top:2rem;font-size:.75rem;color:var(--dim)}
footer a{color:#58a6ff;text-decoration:none}
.pulse{display:inline-block;width:.55em;height:.55em;border-radius:50%;background:var(--green);
margin-right:.5em;animation:p 1.2s ease-in-out infinite}
@keyframes p{0%,100%{opacity:1}50%{opacity:.25}}
@media(prefers-reduced-motion:reduce){.pulse{animation:none}}
</style></head><body><main>
<span class="badge">502 local app down</span>
<h1>tunnel up, <span class="g">local app</span> is not</h1>
<p>The tunnel connection is alive, but nothing answered when we knocked.</p>
<div class="panel">
<div class="k">error</div><div class="v err">` + msg + `</div>
</div>
<div class="tip"><span class="pulse"></span><b>Fix:</b> start your local service and reload -
the tunnel stays connected and keeps retrying for you.</div>
<footer>powered by <a href="https://github.com/topcheer/etunnel">etunnel</a></footer>
</main></body></html>`
}
