package etunnel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"etunnel/protocol"
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

// registerClientStream creates and registers the stream for an incoming Req.
// Must run on the reader goroutine before any stream frames are dispatched.
func (t *Tunnel) registerClientStream(id uint32) *clientStream {
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
	t.mu.Lock()
	select {
	case <-t.closed:
		t.mu.Unlock()
		cancel()
		return nil
	default:
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
	body := "etunnel: local service error: " + msg + "\n"
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
