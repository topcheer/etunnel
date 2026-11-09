package server

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/topcheer/etunnel/protocol"
)

// hop-by-hop headers (RFC 7230) never forwarded end to end.
var hopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

func isHopHeader(k string) bool {
	for _, h := range hopHeaders {
		if strings.EqualFold(k, h) {
			return true
		}
	}
	return false
}

func connectionTokens(h http.Header) map[string]bool {
	tokens := make(map[string]bool)
	for _, v := range h.Values("Connection") {
		for _, tok := range strings.Split(v, ",") {
			tok = strings.TrimSpace(strings.ToLower(tok))
			if tok != "" {
				tokens[tok] = true
			}
		}
	}
	return tokens
}

// hostLabel splits a request host into (subdomain, isBase, isSub).
func (s *Server) hostLabel(hostPort string) (sub string, isBase, isSub bool) {
	host := strings.ToLower(strings.TrimSpace(hostPort))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = strings.ToLower(h)
	}
	domain := strings.ToLower(s.cfg.Domain)
	if host == domain || host == "www."+domain {
		return "", true, false
	}
	suffix := "." + domain
	if strings.HasSuffix(host, suffix) {
		label := strings.TrimSuffix(host, suffix)
		if label != "" && !strings.Contains(label, ".") {
			return label, false, true
		}
	}
	return "", false, false
}

// handleProxy routes external requests to tunnels by hostname.
func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	sub, isBase, isSub := s.hostLabel(r.Host)
	if !isSub {
		if isBase || r.URL.Path == "/" {
			s.landing(w, r)
			return
		}
		s.notFound(w, r)
		return
	}
	sess := s.reg.Lookup(sub)
	if sess == nil {
		s.notFound(w, r)
		return
	}
	s.serveTunnelRequest(w, r, sess)
}

// serveTunnelRequest forwards one external HTTP exchange over a session
// stream.
func (s *Server) serveTunnelRequest(w http.ResponseWriter, r *http.Request, sess *Session) {
	st := sess.OpenStream()
	if st == nil {
		http.Error(w, "tunnel is at its concurrency limit", http.StatusTooManyRequests)
		return
	}
	s.metrics.requestsTotal.Add(1)

	upstreamUpgrade := hasUpgrade(r.Header)

	// Forward header set.
	headers := forwardRequestHeaders(r, upstreamUpgrade)

	if err := sess.sendJSON(&protocol.Req{
		Type:     protocol.MsgReq,
		StreamID: st.id,
		Method:   r.Method,
		Path:     r.URL.RequestURI(),
		Headers:  headers,
	}); err != nil {
		s.badGateway(w, "tunnel disconnected")
		st.abort()
		return
	}

	// Pump the request body. Upgrade requests carry no body and MUST NOT
	// receive a FIN here: after the 101 response the stream stays a raw
	// bidirectional pipe, and FIN would half-close the local connection.
	if !upstreamUpgrade {
		go s.pumpRequestBody(sess, st, r)
	}

	defer func() {
		if !st.finished() {
			_ = sess.sendRst(st.id)
			st.abort()
		}
	}()

	res, err := st.waitRes(s.cfg.HeaderWaitTimeout)
	if err != nil {
		if errors.Is(err, errStreamReset) {
			s.badGateway(w, "tunnel disconnected")
		} else {
			http.Error(w, "tunnel did not answer in time", http.StatusGatewayTimeout)
		}
		return
	}
	select {
	case <-r.Context().Done():
		return // external client went away; deferred cleanup sends RST
	default:
	}

	if res.Status == http.StatusSwitchingProtocols {
		s.serveUpgrade(w, r, sess, st, res)
		return
	}

	// Regular response: copy headers, stream body.
	respHdr := w.Header()
	tokens := connectionTokensFromMap(res.Headers)
	for k, vs := range res.Headers {
		if isHopHeader(k) || tokens[strings.ToLower(k)] {
			continue
		}
		for _, v := range vs {
			respHdr.Add(k, v)
		}
	}
	w.WriteHeader(res.Status)
	if r.Method == http.MethodHead {
		st.finish()
		return
	}

	rc := http.NewResponseController(w)
	buf := make([]byte, protocol.MaxPayload)
	for {
		n, rerr := st.bodyPr.Read(buf)
		if n > 0 {
			if sess.limiter != nil {
				if werr := sess.limiter.WaitN(r.Context(), n); werr != nil {
					panic(http.ErrAbortHandler)
				}
			}
			if _, werr := w.Write(buf[:n]); werr != nil {
				st.abort()
				return
			}
			_ = rc.Flush()
			s.metrics.bytesOut.Add(int64(n))
		}
		if rerr != nil {
			st.finish()
			if errors.Is(rerr, io.EOF) {
				return
			}
			// Mid-body failure after headers were sent: force the connection
			// closed so the client sees a truncated, not silently valid body.
			panic(http.ErrAbortHandler)
		}
	}
}

func hasUpgrade(h http.Header) bool {
	for _, v := range h.Values("Connection") {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
				return true
			}
		}
	}
	return false
}

func forwardRequestHeaders(r *http.Request, isUpgrade bool) map[string][]string {
	out := make(map[string][]string, len(r.Header))
	tokens := connectionTokens(r.Header)
	for k, vs := range r.Header {
		lk := strings.ToLower(k)
		if !isUpgrade {
			if isHopHeader(k) || tokens[lk] {
				continue
			}
		} else {
			// Upgrade requests forward Connection/Upgrade headers verbatim
			// (plus Sec-WebSocket-* which are not hop-listed).
			if lk == "transfer-encoding" || lk == "keep-alive" || lk == "proxy-connection" {
				continue
			}
		}
		cp := make([]string, len(vs))
		copy(cp, vs)
		out[k] = cp
	}
	// Standard proxy headers.
	if prior := out["X-Forwarded-For"]; len(prior) > 0 {
		out["X-Forwarded-For"] = append(prior, remoteIP(r))
	} else {
		out["X-Forwarded-For"] = []string{remoteIP(r)}
	}
	out["X-Forwarded-Proto"] = []string{schemeOf(r)}
	// Original public host (Go keeps r.Host out of r.Header): local apps
	// need it for absolute URLs and Origin/Host same-origin checks. The Go
	// SDK restores it as the request Host; other SDKs read the header.
	if prior := out["X-Forwarded-Host"]; len(prior) > 0 {
		out["X-Forwarded-Host"] = append([]string{r.Host}, prior...)
	} else {
		out["X-Forwarded-Host"] = []string{r.Host}
	}
	if out["X-Real-Ip"] == nil {
		out["X-Real-Ip"] = []string{remoteIP(r)}
	}
	return out
}

func connectionTokensFromMap(h map[string][]string) map[string]bool {
	tokens := make(map[string]bool)
	for _, v := range h["Connection"] {
		for _, tok := range strings.Split(v, ",") {
			tok = strings.TrimSpace(strings.ToLower(tok))
			if tok != "" {
				tokens[tok] = true
			}
		}
	}
	return tokens
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func schemeOf(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func (s *Server) pumpRequestBody(sess *Session, st *Stream, r *http.Request) {
	buf := make([]byte, protocol.MaxPayload)
	for {
		n, err := r.Body.Read(buf)
		if n > 0 {
			if sess.limiter != nil {
				if lerr := sess.limiter.WaitN(st.ctx, n); lerr != nil {
					_ = sess.sendRst(st.id)
					st.abort()
					return
				}
			}
			if serr := sess.sendData(st.id, buf[:n]); serr != nil {
				st.abort()
				return
			}
			s.metrics.bytesIn.Add(int64(n))
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				_ = sess.sendFin(st.id)
			} else {
				_ = sess.sendRst(st.id)
				st.abort()
			}
			return
		}
	}
}

// serveUpgrade handles 101 responses by hijacking the external connection and
// piping raw bytes both ways (WebSocket pass-through).
func (s *Server) serveUpgrade(w http.ResponseWriter, r *http.Request, sess *Session, st *Stream, res *protocol.Res) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		s.badGateway(w, "connection upgrade not supported on this listener")
		st.abort()
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		s.badGateway(w, "hijack failed")
		st.abort()
		return
	}
	defer conn.Close()
	defer st.abort()

	// Write the 101 response with the local service's exact headers.
	var b bytes.Buffer
	fmt.Fprintf(&b, "HTTP/1.1 101 %s\r\n", http.StatusText(res.Status))
	for k, vs := range res.Headers {
		for _, v := range vs {
			fmt.Fprintf(&b, "%s: %s\r\n", k, v)
		}
	}
	b.WriteString("\r\n")
	_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_, _ = brw.Write(b.Bytes())
	_ = brw.Flush()
	_ = conn.SetWriteDeadline(time.Time{})

	done := make(chan struct{}, 2)

	// tunnel -> external. Write+Flush per chunk: the hijacked bufio.Writer
	// otherwise buffers small frames (e.g. a single WebSocket frame) in its
	// 4KB buffer and the TLS-path peer never sees them until close.
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, protocol.MaxPayload)
		for {
			n, rerr := st.bodyPr.Read(buf)
			if n > 0 {
				if _, werr := brw.Write(buf[:n]); werr != nil {
					break
				}
				if ferr := brw.Flush(); ferr != nil {
					break
				}
			}
			if rerr != nil {
				break
			}
		}
		_ = brw.Flush()
		_ = conn.Close() // signal EOF to the other direction
	}()

	// external -> tunnel
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, protocol.MaxPayload)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(s.cfg.StreamIdleTimeout))
			n, rerr := brw.Read(buf)
			if n > 0 {
				if sess.limiter != nil {
					if lerr := sess.limiter.WaitN(sess.ctx, n); lerr != nil {
						_ = conn.Close()
						return
					}
				}
				if serr := sess.sendData(st.id, buf[:n]); serr != nil {
					_ = conn.Close()
					return
				}
				s.metrics.bytesIn.Add(int64(n))
			}
			if rerr != nil {
				_ = sess.sendRst(st.id)
				_ = conn.Close()
				return
			}
		}
	}()

	<-done
	_ = conn.Close()
	<-done
}

func (s *Server) badGateway(w http.ResponseWriter, msg string) {
	http.Error(w, "etunnel: "+msg, http.StatusBadGateway)
}

func (s *Server) landing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, landingHTML(s.cfg))
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, notFoundHTML(s.cfg, r.Host))
}

func landingHTML(cfg Config) string {
	return `<!doctype html>
<html><head><meta charset="utf-8"><link rel="icon" href="data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 512 512' width='512' height='512' role='img' aria-label='etunnel'>  <rect width='512' height='512' rx='96' fill='#0d1117'/>  <rect x='120' y='128' width='272' height='256' rx='40' fill='none' stroke='#e6edf3' stroke-width='40'/>  <line x1='48' y1='256' x2='464' y2='256' stroke='#3fb950' stroke-width='40' stroke-linecap='round'/></svg>"><title>etunnel</title>
<style>body{font-family:ui-monospace,monospace;background:#0b0e14;color:#e6edf3;display:flex;min-height:100vh;align-items:center;justify-content:center;margin:0}main{max-width:640px;padding:2rem}h1{color:#7ee787}code{background:#161b22;padding:.2em .4em;border-radius:4px}pre{background:#161b22;padding:1rem;border-radius:8px;overflow:auto}.brand{display:flex;align-items:center;gap:.7rem;margin:0 0 1.4rem}.brand svg{flex:none}.brand h1{margin:0;font-size:1.5rem}</style>
</head><body><main>
<div class="brand">
  <svg width="48" height="48" viewBox="0 0 512 512" aria-label="etunnel"><rect width="512" height="512" rx="96" fill="#0d1117"/><rect x="120" y="128" width="272" height="256" rx="40" fill="none" stroke="#e6edf3" stroke-width="40"/><line x1="48" y1="256" x2="464" y2="256" stroke="#3fb950" stroke-width="40" stroke-linecap="round"/></svg>
  <h1>etunnel is running</h1>
</div>
<p>Expose your local service to the internet in one line:</p>
<pre>etunnel -s wss://` + cfg.Domain + cfg.WSPath + ` 8080</pre>
<p>or from Go:</p>
<pre>t, _ := etunnel.Connect(ctx, etunnel.Options{Server: "wss://` + cfg.Domain + cfg.WSPath + `", LocalAddr: ":8080"})
fmt.Println(t.URL())</pre>
<p>Anonymous tunnels are free: a random subdomain, 4h lifetime, 1 Mbps.
Register for paid plans (custom subdomains, longer lifetime, 10 Mbps):
<pre>curl -X POST https://` + cfg.Domain + `/api/register -d '{"email":"you@example.com"}'</pre>
</p>
</main></body></html>`
}

func notFoundHTML(cfg Config, host string) string {
	return `<!doctype html>
<html><head><meta charset="utf-8"><title>tunnel not found</title>
<meta http-equiv="refresh" content="5; url=https://github.com/topcheer/etunnel">
<style>body{font-family:ui-monospace,monospace;background:#0b0e14;color:#e6edf3;display:flex;min-height:100vh;align-items:center;justify-content:center;margin:0}main{max-width:560px;padding:2rem;text-align:center}h1{color:#f85149}a{color:#58a6ff}</style>
</head><body><main>
<h1>404 — no tunnel here</h1>
<p><code>` + host + `</code> does not have an active tunnel right now.<br>The SDK client may be offline, or the tunnel expired.</p>
<p><small>Redirecting to <a href="https://github.com/topcheer/etunnel">github.com/topcheer/etunnel</a> in 5 seconds...</small></p>
<script>setTimeout(function(){location.href="https://github.com/topcheer/etunnel"},5000)</script>
</main></body></html>`
}
