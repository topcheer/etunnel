package server

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
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

// isHexDigit reports whether c is an ASCII hexadecimal digit.
func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// looksChunkedFramed reports whether b opens with an HTTP/1.1 chunk-size
// line (RFC 9112 §7.1: 1*HEXDIGIT [;ext] CRLF). Diagnostic only (#286):
// it never mutates the stream - an identity body that merely begins with
// hex digits is at worst logged once.
func looksChunkedFramed(b []byte) bool {
	i := 0
	for i < len(b) && i < 10 && isHexDigit(b[i]) {
		i++
	}
	if i == 0 || i >= len(b) {
		return false // no hexdigits, or they ran to the end of the buffer
	}
	if b[i] == ';' {
		return true // chunk extension after the size
	}
	if b[i] != '\r' {
		return false
	}
	i++
	return i < len(b) && b[i] == '\n'
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
	// Treat the base domain and its registrable root as "base" hosts: with
	// Domain=t.example.com, both example.com and www.example.com serve the
	// landing page alongside t.example.com.
	root := domain
	if i := strings.IndexByte(domain, '.'); i > 0 {
		root = domain[i+1:]
	}
	if host == domain || host == "www."+domain || host == root || host == "www."+root {
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
	// Cluster: another peer may own this subdomain (L4 LB landed us wrong).
	if s.router != nil {
		if target := s.router.Lookup(sub); target != "" {
			if s.router.Forward(sub, w, r) {
				return
			}
			// Forward failed (peer down) - fall through to notFound; the
			// SDK's reconnect will re-land the tunnel on a live peer.
		}
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
	// #187: X-Etunnel-* is this server's namespace. The verified-webhook
	// marker is set by the policy layer below; anything arriving from the
	// visitor is spoofed and must never reach the tunneled app.
	stripEtunnelHeaders(r.Header)
	// Edge policy gate (feature-gap #1/#3): evaluated before any stream is
	// opened, so rejected visitors never reach the tunnel. Webhook rules
	// need the raw body: buffer (bounded), verify, then replay into the
	// normal forwarding path - other policies stay fully streaming.
	var verifyBody []byte
	if sess.policy != nil {
		if sess.policy.needsBody() && r.Body != nil && !hasUpgrade(r.Header) {
			var err error
			verifyBody, err = readBodyForVerify(r, 2<<20)
			if err != nil {
				http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
				return
			}
		}
		if !sess.policy.apply(w, r, verifyBody) {
			return
		}
	}
	st := sess.OpenStream()
	if st == nil {
		http.Error(w, "tunnel is at its concurrency limit", http.StatusTooManyRequests)
		return
	}
	s.metrics.requestsTotal.Add(1)

	upstreamUpgrade := hasUpgrade(r.Header)
	if err := sess.sendJSON(&protocol.Req{
		Type:     protocol.MsgReq,
		StreamID: st.id,
		Method:   r.Method,
		Path:     r.URL.RequestURI(),
		Headers:  forwardRequestHeaders(r, upstreamUpgrade),
	}); err != nil {
		// Proxy failures must leave a trace: accessLog only covers the base
		// domain (#42/#46), which made exactly this class of incident - tunnel
		// up, requests failing - invisible in production logs (#62).
		s.logf("proxy: %s %s%s: send req: %v", r.Method, r.Host, r.URL.RequestURI(), err)
		s.badGateway(w, r.Host, "tunnel disconnected")
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
			// #217: with the response abandoned, a pump parked in
			// r.Body.Read must be released too - the stream cancel alone
			// cannot reach it. A past read deadline unblocks the Read; the
			// connection is unusable for keep-alive at this point anyway
			// (unread body, dead stream).
			_ = http.NewResponseController(w).SetReadDeadline(time.Now())
		}
	}()

	res, err := st.waitRes(s.cfg.HeaderWaitTimeout)
	if err != nil {
		s.serveTunnelResErr(w, r, err)
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

	s.writeTunnelResponseHeaders(w, r, res, st)
	if r.Method == http.MethodHead {
		// #188: HEAD carries no body. finish() above already closed the
		// body pipe (EOF), so returning here flushes the headers and frees
		// the handler instead of parking it in streamTunnelBody forever.
		// #217: the request body is abandoned too. Without this, a visitor
		// can park pumpRequestBody in r.Body.Read forever (goroutine + fd +
		// tunnel bandwidth, uncounted by MaxStreamsPerTunnel - the stream is
		// already removed). A past read deadline unblocks the pump; the
		// connection cannot be reused with an unread body, so keep-alive is
		// not a concern here.
		_ = http.NewResponseController(w).SetReadDeadline(time.Now())
		return
	}
	s.streamTunnelBody(w, r, sess, st, res)
}

// serveTunnelResErr maps a waitRes failure to the visitor response. Reset
// means the tunnel vanished (502); a timeout is the client's stall (504).
func (s *Server) serveTunnelResErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errStreamReset) {
		s.logf("proxy: %s %s%s: stream reset", r.Method, r.Host, r.URL.RequestURI())
		s.badGateway(w, r.Host, "tunnel disconnected")
		return
	}
	s.logf("proxy: %s %s%s: no client response within %s (client stalled or gone)",
		r.Method, r.Host, r.URL.RequestURI(), s.cfg.HeaderWaitTimeout)
	http.Error(w, "tunnel did not answer in time", http.StatusGatewayTimeout)
}

// writeTunnelResponseHeaders copies the client's response headers through,
// minus hop-by-hop and any connection tokens, then commits the status.
func (s *Server) writeTunnelResponseHeaders(w http.ResponseWriter, r *http.Request, res *protocol.Res, st *Stream) {
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
	status := res.Status
	if status < 100 || status > 999 {
		// #200: res.Status is tunnel-client controlled; net/http panics on
		// out-of-range codes. Answer 502 instead of killing the connection.
		s.logf("proxy: %s %s%s: client sent invalid status %d", r.Method, r.Host, r.URL.RequestURI(), status)
		status = http.StatusBadGateway
	}
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		st.finish()
	}
}

// streamTunnelBody relays the stream body to the visitor under the account
// bandwidth limiter. A mid-body failure after headers forces the connection
// closed (panic ErrAbortHandler) so the client sees a truncated - not
// silently valid - body; EOF finishes the stream cleanly.
func (s *Server) streamTunnelBody(w http.ResponseWriter, r *http.Request, sess *Session, st *Stream, res *protocol.Res) {
	rc := http.NewResponseController(w)
	// #286: the wire contract (PROTOCOL.md §2) is that res DATA frames carry
	// the identity entity body - the replay stack's HTTP client owns the
	// transfer coding, so chunk-size lines never cross the tunnel. When the
	// response declares no length and no framing while the bytes open with a
	// chunk-size line, a replay stack forwarded the local service's
	// transfer-framed bytes verbatim; behind h2/CDN the outer framing is
	// invisible and the visitor gunzips framing+payload (browser content
	// decode errors, burrow 2026-11-21). The proxy must not second-guess
	// payload bytes, so the body is relayed untouched - but the contract
	// violation gets a pointed trace here instead of surfacing only as a
	// broken page on the visitor side.
	sniffFraming := len(res.Headers["Content-Length"]) == 0 &&
		len(res.Headers["Transfer-Encoding"]) == 0
	sniffed := false
	// #294 F2: refreshed before every write, so healthy long transfers keep
	// moving while a stalled visitor (TCP zero window) cannot pin the
	// stream, its goroutine and the local connection indefinitely - exactly
	// the per-write deadline config.go documents for StreamIdleTimeout.
	deadlineSupported := true
	buf := make([]byte, protocol.MaxPayload)
	for {
		n, rerr := st.bodyPr.Read(buf)
		if n > 0 {
			if !sniffed {
				sniffed = true
				if sniffFraming && looksChunkedFramed(buf[:n]) {
					s.logf("proxy: %s %s%s: response body starts with chunk-size framing but headers declare no framing - tunnel client replay leaked transfer-framed bytes (identity-body contract: PROTOCOL.md §2, #286)", r.Method, r.Host, r.URL.RequestURI())
				}
			}
			if sess.limiter != nil {
				if werr := sess.limiter.WaitN(r.Context(), n); werr != nil {
					panic(http.ErrAbortHandler)
				}
			}
			// Unsupported writers (httptest recorders in unit tests) simply
			// run without a deadline.
			if deadlineSupported {
				if derr := rc.SetWriteDeadline(time.Now().Add(s.cfg.StreamIdleTimeout)); derr != nil {
					deadlineSupported = false
				}
			}
			if _, werr := w.Write(buf[:n]); werr != nil {
				st.abort()
				if errors.Is(werr, os.ErrDeadlineExceeded) {
					// #294 F2: the visitor stalled past StreamIdleTimeout -
					// abort with the usual mid-body semantics (truncated,
					// never silently valid).
					panic(http.ErrAbortHandler)
				}
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
			panic(http.ErrAbortHandler)
		}
	}
}

// stripEtunnelHeaders deletes every X-Etunnel-* header from an inbound
// request (#187): the only legitimate source of the verified-webhook marker
// is the policy layer after signature verification.
func stripEtunnelHeaders(h http.Header) {
	for k := range h {
		if strings.HasPrefix(strings.ToLower(k), "x-etunnel-") {
			h.Del(k)
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
	// Standard proxy headers. This binary is the network edge: an inbound
	// X-Forwarded-For / X-Forwarded-Host / X-Real-Ip was set by whoever made
	// the request, so it is untrusted and replaced rather than extended
	// (issue #13). Downstream apps can rely on exactly one authoritative hop.
	out["X-Forwarded-For"] = []string{remoteIP(r)}
	out["X-Forwarded-Proto"] = []string{schemeOf(r)}
	// Original public host (Go keeps r.Host out of r.Header): local apps
	// need it for absolute URLs and Origin/Host same-origin checks. The Go
	// SDK restores it as the request Host; other SDKs read the header.
	out["X-Forwarded-Host"] = []string{r.Host}
	out["X-Real-Ip"] = []string{remoteIP(r)}
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
		if st.finished() {
			// #217: the response completed without this body (HEAD, early
			// finish). Sending more data would target a removed stream, so
			// just stop; the caller has also set a read deadline that
			// unblocks a Read parked between checks.
			return
		}
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
		s.badGateway(w, r.Host, "connection upgrade not supported on this listener")
		st.abort()
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		s.badGateway(w, r.Host, "hijack failed")
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
				// #294 F1: the downlink direction meters and bills exactly
				// like the uplink below and streamTunnelBody - tier.Bandwidth
				// applies to upgraded streams too. WaitN cancelled (session
				// gone / over limit) stops delivery, mirroring the uplink.
				if sess.limiter != nil {
					if lerr := sess.limiter.WaitN(sess.ctx, n); lerr != nil {
						break
					}
				}
				if _, werr := brw.Write(buf[:n]); werr != nil {
					break
				}
				if ferr := brw.Flush(); ferr != nil {
					break
				}
				s.metrics.bytesOut.Add(int64(n))
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

func (s *Server) badGateway(w http.ResponseWriter, host, msg string) {
	// #194: host and msg end up inside generated HTML; escape both (host is
	// visitor-controlled via the Host header).
	host = htmlEscapeText(strings.Split(host, ":")[0])
	msg = htmlEscapeText(msg)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	_, _ = io.WriteString(w, `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>502 - upstream down</title>
<style>
:root{--bg:#f8f3e7;--panel:#fffdf6;--line:#e6ddc6;--fg:#242b21;--dim:#7a7f6e;--green:#2c6e49}
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:'Nunito Sans','Avenir Next','Segoe UI',sans-serif;background:var(--bg);color:var(--fg);
display:flex;min-height:100vh;align-items:center;justify-content:center;line-height:1.65}
main{max-width:600px;padding:2.5rem 1.5rem;width:100%}
.badge{display:inline-block;border:1px solid rgba(191,75,55,.4);color:#bf4b37;border-radius:99px;
padding:.2rem .8rem;font-size:.72rem;letter-spacing:.08em;text-transform:uppercase;margin-bottom:1.1rem}
h1{font-family:'Fraunces',Georgia,serif;font-size:1.7rem;letter-spacing:-.01em;margin-bottom:.4rem}
h1 .g{color:var(--green)}
p{color:var(--dim);font-size:.9rem;margin-bottom:1.4rem}
.panel{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:1.1rem 1.3rem;margin-bottom:1.1rem}
.panel .k{color:var(--dim);font-size:.72rem;text-transform:uppercase;letter-spacing:.08em;margin-bottom:.3rem}
.panel .v{font-size:.9rem;word-break:break-all}
.err{color:#bf4b37}
.tip{border-left:3px solid var(--green);padding:.8rem 1rem;margin-top:1.2rem;font-size:.82rem;color:var(--dim)}
.tip b{color:var(--fg)}
footer{margin-top:2rem;font-size:.75rem;color:var(--dim)}
footer a{color:#2c6e49;text-decoration:none}
.pulse{display:inline-block;width:.55em;height:.55em;border-radius:50%;background:var(--green);
margin-right:.5em;animation:p 1.2s ease-in-out infinite}
@keyframes p{0%,100%{opacity:1}50%{opacity:.25}}
@media(prefers-reduced-motion:reduce){.pulse{animation:none}}
</style></head><body><main>
<span class="badge">502 upstream down</span>
<h1>tunnel up, <span class="g">local app</span> is not</h1>
<p>The etunnel connection is working, but nothing answered on the other end of it.</p>
<div class="panel">
<div class="k">host</div><div class="v">`+host+`</div>
<div class="k" style="margin-top:.8rem">error</div><div class="v err">`+msg+`</div>
</div>
<div class="tip"><span class="pulse"></span><b>It comes back by itself.</b> If the CLI is running
with reconnect on, the tunnel keeps retrying the server side; start your local
service and reload this page - no need to restart anything.</div>
<footer>powered by <a href="https://github.com/topcheer/etunnel">etunnel</a></footer>
</main></body></html>`)
}

func (s *Server) landing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, landingHTML(s.cfg, pickLang(r)))
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, notFoundHTML(s.cfg, r))
}

func landingHTML(cfg Config, lang string) string {
	server := "wss://" + cfg.Domain + cfg.WSPath
	qsTabs, qsPanels := quickstartHTML(server)
	t := func(key string) string { return T(lang, key) }
	return `<!doctype html>
<html lang="` + lang + `" dir="` + langDir(lang) + `"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="description" content="` + t("land.meta.desc") + `">
<link rel="icon" href="` + portalFavicon + `">
<title>` + t("land.title") + `</title>
<style>
@import url('https://fonts.googleapis.com/css2?family=Fraunces:ital,opsz,wght@0,9..144,600;0,9..144,800;1,9..144,600&family=Nunito+Sans:ital,wght@0,400;0,600;0,700;0,800&display=swap');
:root{--bg:#f8f3e7;--panel:#fffdf6;--panel2:#f1ead7;--line:#e6ddc6;--fg:#242b21;--dim:#7a7f6e;--green:#2c6e49;--green2:#4a8f6a;--amber:#d9783f;--red:#bf4b37;--code:#22271e}
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:'Nunito Sans','Avenir Next','Segoe UI',sans-serif;background:var(--bg);color:var(--fg);line-height:1.65}
a{color:var(--green);text-decoration:none}a:hover{text-decoration:underline}
/* nav */
nav{display:flex;align-items:center;justify-content:space-between;padding:1rem 2rem;border-bottom:1px solid var(--line);background:rgba(248,243,231,.88);backdrop-filter:blur(8px);position:sticky;top:0;z-index:10}
nav .logo{display:flex;align-items:center;gap:.6rem;font-weight:800;font-size:1.08rem;color:var(--fg);font-family:'Fraunces',Georgia,serif}
nav .links{display:flex;gap:1.5rem;font-size:.88rem}
nav .links a{color:var(--dim)}nav .links a:hover{color:var(--fg);text-decoration:none}
/* hero: warm paper with soft washes + grain */
.hero{position:relative;padding:5.5rem 1.5rem 4rem;overflow:hidden;text-align:center;border-bottom:1px solid var(--line)}
.hero::before{content:"";position:absolute;inset:0;pointer-events:none;background:radial-gradient(ellipse 55% 45% at 50% -8%,rgba(74,143,106,.16),transparent 62%),radial-gradient(ellipse 40% 38% at 88% 108%,rgba(217,120,63,.10),transparent 65%)}
.hero::after{content:"";position:absolute;inset:0;pointer-events:none;opacity:.5;background-image:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='140' height='140'%3E%3Cfilter id='n'%3E%3CfeTurbulence type='fractalNoise' baseFrequency='0.9' numOctaves='2'/%3E%3CfeColorMatrix values='0 0 0 0 0.14 0 0 0 0 0.13 0 0 0 0 0.10 0 0 0 0.045 0'/%3E%3C/filter%3E%3Crect width='140' height='140' filter='url(%23n)'/%3E%3C/svg%3E")}
.hero .inner{position:relative;max-width:860px;margin:0 auto;z-index:1}
.hero .badge{display:inline-block;border:1px solid var(--line);background:var(--panel);border-radius:99px;padding:.32rem .95rem;font-size:.78rem;color:var(--dim);margin-bottom:1.4rem;font-weight:600}
.hero h1{font-family:'Fraunces',Georgia,serif;font-size:clamp(2.2rem,6vw,3.9rem);font-weight:800;letter-spacing:-.02em;line-height:1.08;margin-bottom:1.1rem}
.hero h1 .g{color:var(--green);font-style:italic}
.hero p{color:var(--dim);font-size:1.06rem;max-width:36rem;margin:0 auto 2rem}
.cmd{display:flex;align-items:center;gap:.8rem;background:var(--code);color:#e9e2cd;border-radius:14px;padding:.9rem 1.2rem;max-width:30rem;margin:0 auto 2rem;font-size:.95rem;font-family:ui-monospace,'SF Mono',Menlo,Consolas,monospace;box-shadow:inset 0 0 0 1px rgba(246,239,221,.08),0 18px 40px -18px rgba(36,43,33,.5)}
.cmd .dollar{color:#9fd8ae}
.cmd .copy{margin-left:auto;color:#8b917f;cursor:pointer;font-size:.76rem;border:1px solid rgba(246,239,221,.18);border-radius:7px;padding:.18rem .55rem;background:none;font-family:inherit}
.cmd .copy:hover{color:#e9e2cd;border-color:rgba(246,239,221,.4)}
.cta{display:flex;gap:1rem;justify-content:center;flex-wrap:wrap}
.btn{display:inline-block;border-radius:999px;padding:.78rem 1.6rem;font-weight:800;font-size:.95rem;border:1px solid transparent;transition:transform .12s,box-shadow .15s,background .15s}
.btn.primary{background:var(--green);color:#f8f3e7}.btn.primary:hover{background:var(--green2);text-decoration:none;transform:translateY(-1px);box-shadow:0 8px 20px -8px rgba(44,110,73,.5)}
.btn.ghost{border-color:var(--line);color:var(--fg);background:var(--panel)}.btn.ghost:hover{border-color:var(--green2);text-decoration:none;transform:translateY(-1px)}
/* hero video in a framed window - the paper page's one dark object */
.win{position:relative;z-index:1;max-width:720px;margin:3.2rem auto 0;border-radius:18px;background:var(--code);padding:.55rem .55rem 0;box-shadow:0 2px 6px rgba(36,43,33,.15),0 40px 80px -30px rgba(36,43,33,.45)}
.win .bar{display:flex;align-items:center;gap:.4rem;padding:0 .5rem .5rem}
.win .bar i{width:10px;height:10px;border-radius:50%;background:#4a5142}
.win .bar i:nth-child(1){background:#d9783f}.win .bar i:nth-child(2){background:#d9c26a}.win .bar i:nth-child(3){background:#4a8f6a}
.win .bar span{margin-left:auto;color:#8b917f;font-size:.7rem;font-family:ui-monospace,Menlo,monospace}
.win video{display:block;width:100%;border-radius:13px 13px 0 0}
/* staggered reveal */
.hero .badge,.hero h1,.hero p,.hero .cmd,.hero .cta,.hero .win{animation:rise .7s cubic-bezier(.2,.7,.2,1) both}
.hero h1{animation-delay:.06s}.hero p{animation-delay:.12s}.hero .cmd{animation-delay:.18s}.hero .cta{animation-delay:.24s}.hero .win{animation-delay:.34s}
@keyframes rise{from{opacity:0;transform:translateY(18px)}to{opacity:1;transform:none}}
@media(prefers-reduced-motion:reduce){.hero .badge,.hero h1,.hero p,.hero .cmd,.hero .cta,.hero .win{animation:none}}
/* sections */
section{max-width:1080px;margin:0 auto;padding:4.5rem 1.5rem}
section h2{font-family:'Fraunces',Georgia,serif;font-size:1.7rem;letter-spacing:-.015em;margin-bottom:.5rem}
section .sub{color:var(--dim);margin-bottom:2.2rem;font-size:.98rem}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(250px,1fr));gap:1rem}
.card{background:var(--panel);border:1px solid var(--line);border-radius:16px;padding:1.5rem;transition:transform .15s,box-shadow .2s}
.card:hover{transform:translateY(-3px);box-shadow:0 3px 8px rgba(36,43,33,.05),0 20px 44px -24px rgba(36,43,33,.3)}
.card .ic{font-size:1.5rem;margin-bottom:.7rem}
.card h3{font-family:'Fraunces',Georgia,serif;font-size:1.08rem;margin-bottom:.4rem}
.card p{color:var(--dim);font-size:.88rem}
/* code demo */
.demo{display:grid;grid-template-columns:1fr 1fr;gap:1rem}
@media(max-width:760px){.demo{grid-template-columns:1fr}}
pre{background:var(--code);color:#e9e2cd;border-radius:14px;padding:1.1rem 1.2rem;font-size:.83rem;overflow:auto;line-height:1.75;font-family:ui-monospace,'SF Mono',Menlo,Consolas,monospace}
pre .cm{color:#8b917f}pre .k{color:#f0a35e}pre .s{color:#b5d99a}pre .f{color:#e3c078}pre .g{color:#9fd8ae}
pre .out{color:#9fd8ae}
/* quickstart tabs */
.tabs{display:flex;gap:.4rem;flex-wrap:wrap;margin-bottom:1rem}
.tabs button.qtab{background:var(--panel);border:1px solid var(--line);color:var(--dim);border-radius:999px;padding:.4rem .85rem;font-size:.8rem;font-family:inherit;font-weight:600;cursor:pointer;transition:border-color .15s,color .15s}
.tabs button.qtab.on{background:var(--green);border-color:var(--green);color:#f8f3e7}
.tabs button.qtab:hover{color:var(--fg);border-color:var(--green2)}
.panel{display:none}
.panel.on{display:block}
/* code snippet */
.snippet{background:var(--code);border-radius:16px;overflow:hidden;max-width:52rem;margin:0 auto;box-shadow:0 30px 70px -30px rgba(36,43,33,.45)}
.snip-head{display:flex;align-items:center;gap:.7rem;flex-wrap:wrap;padding:.65rem 1rem;border-bottom:1px solid rgba(246,239,221,.1);font-size:.78rem;color:#e9e2cd}
.snip-head .fname{color:#e9e2cd;font-weight:700}
.snip-head .mode{font-size:.64rem;text-transform:uppercase;letter-spacing:.09em;border-radius:6px;padding:.08rem .45rem;border:1px solid rgba(246,239,221,.2);color:#8b917f}
.snip-head .mode.embed{color:#9fd8ae;border-color:rgba(74,143,106,.55)}
.snip-head .dep{color:#8b917f;flex:1 1 auto;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.snip-head .snip-link{margin-left:auto;color:#9fd8ae;font-size:.75rem}
.snip-head .snip-copy{background:none;border:1px solid rgba(246,239,221,.2);border-radius:7px;color:#8b917f;font-family:inherit;font-size:.7rem;padding:.12rem .5rem;cursor:pointer}
.snip-head .snip-copy:hover{color:#e9e2cd;border-color:rgba(246,239,221,.4)}
pre.code{margin:0;border:0;border-radius:0;background:transparent;padding:1rem 1.1rem;font-size:.8rem;line-height:1.72;overflow:auto;tab-size:4}
pre.code .kw{color:#f0a35e}
pre.code .st{color:#b5d99a}
pre.code .cm{color:#8b917f;font-style:italic}
/* diagrams */
.flow{background:var(--panel);border:1px solid var(--line);border-radius:16px;padding:1.5rem;overflow:auto;text-align:center}
.flow pre.mermaid{background:transparent;border:0;margin:0;padding:0;font-size:.8rem;color:var(--dim);min-width:640px}
/* narrow screens: the vertical variant fits, so it needs no minimum width */
@media(max-width:700px){.flow pre.mermaid{min-width:0}}
/* narrow screens: the diagram keeps its readable size and pans inside .flow */
@media(max-width:700px){
  nav{flex-wrap:wrap;justify-content:center;gap:.45rem;padding:.75rem 1rem}
  nav .links{order:3;width:100%;justify-content:center;gap:1.1rem;font-size:.8rem}
  section{padding:3rem 1rem}
  .flow{padding:1rem}
  .snip-head .snip-link{margin-left:auto}
  pre.code{font-size:.75rem}
  .cmd{font-size:.85rem}
  .hero{padding:3.5rem 1rem 3rem}
}
/* pricing strip */
.tiers{display:grid;grid-template-columns:repeat(auto-fit,minmax(240px,1fr));gap:1rem}
.tier{background:var(--panel);border:1px solid var(--line);border-radius:16px;padding:1.6rem;transition:transform .15s,box-shadow .2s}
.tier:hover{transform:translateY(-3px);box-shadow:0 20px 44px -24px rgba(36,43,33,.3)}
.tier .name{font-family:'Fraunces',Georgia,serif;font-weight:800;font-size:1.1rem}
.tier .price{font-family:'Fraunces',Georgia,serif;font-size:2rem;font-weight:800;margin:.5rem 0 .3rem}
.tier .price small{font-size:.8rem;color:var(--dim);font-weight:400}
.tier ul{list-style:none;margin:.9rem 0 1.3rem;color:var(--dim);font-size:.86rem}
.tier ul li::before{content:"✓ ";color:var(--green);font-weight:800}
.tier.pro{border-color:rgba(74,143,106,.55);box-shadow:0 0 0 3px rgba(74,143,106,.08)}
/* footer */
footer{border-top:1px solid var(--line);padding:2rem;text-align:center;color:var(--dim);font-size:.8rem;background:var(--panel2)}
footer .sep{margin:0 .6rem}
/* AI agent integration block */
.agentbox{background:var(--panel);border:1px solid var(--line);border-radius:16px;max-width:52rem;margin:2.6rem auto 0;overflow:hidden;box-shadow:0 20px 44px -24px rgba(36,43,33,.28)}
.agenthead{display:flex;align-items:center;gap:.9rem;padding:1rem 1.2rem;border-bottom:1px solid var(--line);background:var(--panel2)}
.agenttag{flex:none;font-family:'Fraunces',Georgia,serif;font-weight:800;font-size:.95rem;color:#f8f3e7;background:var(--green);border-radius:10px;padding:.35rem .6rem}
.agenthead h3{font-family:'Fraunces',Georgia,serif;font-size:1.02rem;margin-bottom:.1rem}
.agenthead p{color:var(--dim);font-size:.78rem}
.agentcopy{margin-left:auto;padding:.5rem 1rem;font-size:.8rem}
.agentpeek{margin:0;border-radius:0;background:var(--code);color:#e9e2cd;padding:1rem 1.2rem;font-size:.72rem;line-height:1.6;max-height:11.5rem;overflow:hidden;position:relative;white-space:pre-wrap;word-break:break-word}
.agentpeek::after{content:"";position:absolute;left:0;right:0;bottom:0;height:3.2rem;background:linear-gradient(transparent,var(--code))}
.agentfoot{padding:.6rem 1.2rem .8rem;color:var(--dim);font-size:.72rem;border-top:1px solid rgba(246,239,221,.06)}
@media(max-width:700px){.agenthead{flex-wrap:wrap}.agentcopy{margin-left:0}}
</style></head><body>
<nav>
  <div class="logo">
    <svg width="26" height="26" viewBox="0 0 512 512"><rect width="512" height="512" rx="120" fill="#242b21"/><rect x="120" y="128" width="272" height="256" rx="40" fill="none" stroke="#f6efdd" stroke-width="40"/><line x1="48" y1="256" x2="464" y2="256" stroke="#4a8f6a" stroke-width="40" stroke-linecap="round"/></svg>
    etunnel
  </div>
  <div class="links">
    <a href="#features">` + t("nav.features") + `</a><a href="#quickstart">` + t("nav.quickstart") + `</a><a href="#pricing">` + t("nav.pricing") + `</a><a href="https://github.com/topcheer/etunnel">github</a>
  </div>
</nav>

<div class="hero">
  <div class="inner">
    <div class="badge">` + t("land.badge") + `</div>
    <h1>` + t("land.h1.pre") + `<span class="g">` + t("land.h1.em") + `</span></h1>
    <p>` + t("land.sub") + `</p>
    <div class="cmd"><span class="dollar">$</span><span id="c">npx @easytunnel/et-sdk 3000</span><button class="copy" onclick="navigator.clipboard().writeText?navigator.clipboard.writeText(document.getElementById('c').textContent):0;this.textContent='` + t("btn.copied") + `'">` + t("btn.copy") + `</button></div>
    <div class="cta">
      <a class="btn primary" href="/pricing">` + t("land.cta.main") + `</a>
      <a class="btn ghost" href="https://github.com/topcheer/etunnel">` + t("land.cta.gh") + `</a>
    </div>
  </div>
  <figure class="win">
    <div class="bar"><i></i><i></i><i></i><span>t.etun.uk</span></div>
    <video autoplay muted loop playsinline preload="metadata">
      <source src="/assets/hero-loop.webm" type="video/webm">
      <source src="/assets/hero-loop.mp4" type="video/mp4">
    </video>
  </figure>
</div>

<section id="features">
  <h2>` + t("land.feat.h") + `</h2>
  <p class="sub">` + t("land.feat.sub") + `</p>
  <div class="grid">
    <div class="card"><div class="ic">&#128274;</div><h3>` + t("land.f1.h") + `</h3><p>` + t("land.f1.p") + `</p></div>
    <div class="card"><div class="ic">&#9889;</div><h3>` + t("land.f2.h") + `</h3><p>` + t("land.f2.p") + `</p></div>
    <div class="card"><div class="ic">&#128187;</div><h3>` + t("land.f3.h") + `</h3><p>` + t("land.f3.p") + `</p></div>
    <div class="card"><div class="ic">&#128200;</div><h3>` + t("land.f4.h") + `</h3><p>` + t("land.f4.p") + `</p></div>
    <div class="card"><div class="ic">&#127760;</div><h3>` + t("land.f5.h") + `</h3><p>` + t("land.f5.p") + `</p></div>
    <div class="card"><div class="ic">&#128179;</div><h3>` + t("land.f6.h") + `</h3><p>` + t("land.f6.p") + `</p></div>
  </div>
</section>

<section id="how">
  <h2>` + t("land.how.h") + `</h2>
  <p class="sub">` + t("land.how.sub") + `</p>
  <div class="flow">
    <pre class="mermaid" id="flowdiagram" data-mobile="flowchart TD
  V[&quot;visitor<br/>https://quiet-frog-42.t.etun.uk&quot;] -->|HTTPS 443| E[&quot;etunneld<br/>wildcard TLS + plan limits&quot;]
  E -->|&quot;wss /tunnel<br/>multiplexed streams&quot;| C[&quot;SDK client<br/>inside your process&quot;]
  C -->|HTTP/1.1| A[&quot;localhost:3000<br/>your app&quot;]
  E -.->|&quot;101 upgrade<br/>raw byte pipe&quot;| A">
flowchart LR
  V["visitor<br/>https://quiet-frog-42.t.etun.uk"] -->|HTTPS 443| E["etunneld<br/>wildcard TLS + plan limits"]
  E -->|wss /tunnel<br/>multiplexed streams| C["SDK client<br/>inside your process"]
  C -->|HTTP/1.1| A["localhost:3000<br/>your app"]
  E -.->|101 upgrade<br/>raw byte pipe| A
    </pre>
  </div>
  <div class="grid" style="margin-top:1rem">
    <div class="card"><h3>` + t("land.how.1.h") + `</h3><p>` + t("land.how.1.p") + `</p></div>
    <div class="card"><h3>` + t("land.how.2.h") + `</h3><p>` + t("land.how.2.p") + `</p></div>
    <div class="card"><h3>` + t("land.how.3.h") + `</h3><p>` + t("land.how.3.p") + `</p></div>
  </div>
</section>

<section id="quickstart">
  <h2>` + t("land.qs.h") + `</h2>
  <p class="sub">` + t("land.qs.sub") + `</p>
  <div class="tabs" id="langtabs">` + qsTabs + `</div>
  ` + qsPanels + `
  <p class="sub" style="text-align:center;margin:1.4rem 0 0">` + t("land.qs.note") + `</p>

  <div class="agentbox" id="agentbox">
    <div class="agenthead">
      <span class="agenttag">AI</span>
      <div>
        <h3>` + t("land.agent.h") + `</h3>
        <p>` + t("land.agent.sub") + `</p>
      </div>
      <button class="btn primary agentcopy" id="agentcopy" onclick="copyAgent(this)">` + t("land.agent.copy") + `</button>
    </div>
    <pre class="agentpeek" id="agentpeek"></pre>
    <p class="agentfoot">` + t("land.agent.foot") + `</p>
  </div>
</section>

<section id="pricing">
  <h2>` + t("land.price.h") + `</h2>
  <p class="sub">` + t("land.price.sub") + `</p>
  <div class="tiers">
    <div class="tier"><div class="name">` + t("land.tier.free") + `</div><div class="price">$0</div><ul><li>` + t("land.tier.free.li1") + `</li><li>` + t("land.tier.free.li2") + `</li><li>` + t("land.tier.free.li3") + `</li><li>` + t("land.tier.free.li4") + `</li></ul><a class="btn ghost" href="#quickstart">` + t("land.tier.free.btn") + `</a></div>
    <div class="tier pro"><div class="name">` + t("land.tier.starter") + `</div><div class="price">$3<small>/mo</small></div><ul><li>` + t("land.tier.starter.li1") + `</li><li>` + t("land.tier.starter.li2") + `</li><li>` + t("land.tier.starter.li3") + `</li><li>` + t("land.tier.starter.li4") + `</li></ul><a class="btn primary" href="/pricing">` + t("land.tier.btn.sub") + `</a></div>
    <div class="tier"><div class="name">` + t("land.tier.pro") + `</div><div class="price">$9<small>/mo</small></div><ul><li>` + t("land.tier.pro.li1") + `</li><li>` + t("land.tier.pro.li2") + `</li><li>` + t("land.tier.pro.li3") + `</li><li>` + t("land.tier.pro.li4") + `</li></ul><a class="btn primary" href="/pricing">` + t("land.tier.btn.sub") + `</a></div>
  </div>
</section>

<footer>
  <span>` + cfg.Domain + `</span><span class="sep">&middot;</span><a href="https://github.com/topcheer/etunnel">GitHub</a><span class="sep">&middot;</span><a href="https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md">Protocol</a><span class="sep">&middot;</span><a href="/pricing">Pricing</a><span class="sep">&middot;</span><a href="/terms">Terms</a><span class="sep">&middot;</span><a href="/privacy">Privacy</a>
  <br><span style="opacity:.6">` + t("foot.tagline") + `</span>
  <div class="langrow">` + langSwitchHTML(lang) + `</div>
<script>
function showLang(btn){
  var id = btn.getAttribute('data-id');
  var tabs = document.querySelectorAll('#langtabs button');
  for (var i = 0; i < tabs.length; i++) tabs[i].classList.toggle('on', tabs[i] === btn);
  var ps = document.querySelectorAll('.panel');
  for (var j = 0; j < ps.length; j++) ps[j].classList.toggle('on', ps[j].id === 'p-' + id);
}
function copySnippet(btn){
  var pre = btn.closest('.snippet').querySelector('pre.code');
  var done = function(){ btn.textContent = 'copied'; setTimeout(function(){ btn.textContent = 'copy'; }, 1500); };
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(pre.textContent).then(done, done);
  } else { done(); }
}
// AI agent block: the page shows only a faded peek; the button copies the
// FULL prompt hidden in the #agentprompt script tag (full fidelity in the
// clipboard). The tag sits after this script, so populate on DOM ready.
var agentPrompt = '';
document.addEventListener('DOMContentLoaded', function(){
  agentPrompt = document.getElementById('agentprompt').textContent.trim();
  document.getElementById('agentpeek').textContent = agentPrompt;
});
function copyAgent(btn){
  var done = function(){ btn.textContent='` + t("btn.copied") + `'; setTimeout(function(){ btn.textContent='` + t("land.agent.copy") + `'; }, 1800); };
  if (navigator.clipboard && navigator.clipboard.writeText && agentPrompt) {
    navigator.clipboard.writeText(agentPrompt).then(done, done);
  } else { done(); }
}
</script>
<script type="text/plain" id="agentprompt">` + agentPromptText(cfg) + `</script>
<script>
// Pick the diagram orientation before mermaid boots: a top-down flow fits a
// phone without panning, the left-to-right one reads better on desktop.
(function () {
  var el = document.getElementById('flowdiagram');
  if (el && window.matchMedia('(max-width:700px)').matches && el.dataset.mobile) {
    el.textContent = el.dataset.mobile;
  }
})();
</script>
<script type="module">
import mermaid from 'https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.esm.min.mjs';
mermaid.initialize({startOnLoad:true,theme:'base',themeVariables:{fontFamily:"'Nunito Sans',sans-serif",fontSize:'14px',lineColor:'#b9ae90',textColor:'#242b21',primaryColor:'#fffdf6',primaryTextColor:'#242b21',primaryBorderColor:'#2c6e49',edgeLabelBackground:'#f8f3e7',clusterBkg:'#f1ead7',clusterBorder:'#e6ddc6'}});
</script>
</footer>
</body></html>`
}

// agentPromptText renders the full copy-to-clipboard integration prompt for
// AI agents. The landing page shows only a faded peek; the copy button puts
// this complete text on the clipboard.
func agentPromptText(cfg Config) string {
	server := "wss://" + cfg.Domain + cfg.WSPath
	return `You have access to etunnel, a public tunnel service: it exposes a local HTTP service to the public internet over an outbound WebSocket - no inbound ports, no router config.

## Use it when
- You need to show the user a running web app/demo/webhook receiver right now.
- An external service must reach the machine (OAuth callbacks, payment webhooks, CI callbacks).
- A mobile app or remote client needs to hit a local API.

## How to expose port 3000 (no account needed)
Run the CLI once and parse stdout:
  npx @easytunnel/et-sdk 3000
or use the TypeScript SDK from any Node process:
  const { connect } = require('@easytunnel/et-sdk');
  const tunnel = await connect('` + server + `', 3000);
  console.log(tunnel.url); // e.g. https://quiet-frog-42.` + cfg.Domain + `
Other languages: Go, Python, Rust, Java, Swift, Dart, PHP, Elixir and more - same two-argument connect(), see github.com/topcheer/etunnel (sdks/).

## Rules
1. The URL is live only while the exposing process runs. Keep it alive while needed; stop it after.
2. Free tunnels: random subdomain, 4-hour lifetime, 1 Mbps, HTTPS + WebSocket pass-through. Do not promise a stable URL.
3. Never tunnel secrets, credentials, admin panels or anything private on a public URL without the user's explicit request.
4. If the user wants a permanent/custom subdomain, they need an API key (t.etun.uk -> sign in -> /me); pass it as the token argument. Do not fabricate keys.
5. The local service sees normal HTTP requests; the visitor's real IP arrives in X-Real-IP / X-Forwarded-For set by the tunnel server.

## Failure modes
- No URL printed / handshake error: check the port is actually listening, then retry once.
- 404 on the tunnel URL: the exposing process died - restart it.
- Slow responses: free tier is 1 Mbps; large assets will crawl.

Current server: ` + server + `
Docs & SDKs: github.com/topcheer/etunnel`
}

func notFoundHTML(cfg Config, r *http.Request) string {
	lang := pickLang(r)
	t := func(key string) string { return T(lang, key) }
	host := htmlEscapeText(r.Host) // #194: r.Host is visitor-controlled
	return `<!doctype html>
<html><head><meta charset="utf-8"><title>` + t("nf.title") + `</title>
<meta http-equiv="refresh" content="5; url=https://github.com/topcheer/etunnel">
<style>body{font-family:'Nunito Sans','Avenir Next',sans-serif;background:#f8f3e7;color:#242b21;display:flex;min-height:100vh;align-items:center;justify-content:center;margin:0}main{max-width:560px;padding:2rem;text-align:center}h1{color:#bf4b37;font-family:'Fraunces',Georgia,serif}a{color:#2c6e49}code{background:#22271e;color:#e9e2cd;border-radius:6px;padding:.1rem .4rem;font-family:ui-monospace,Menlo,monospace;font-size:.85em}</style>
</head><body><main>
<h1>404 — no tunnel here</h1>
<p><code>` + host + `</code> does not have an active tunnel right now.<br>The SDK client may be offline, or the tunnel expired.</p>
<p><small>Redirecting to <a href="https://github.com/topcheer/etunnel">github.com/topcheer/etunnel</a> in 5 seconds...</small></p>
<script>setTimeout(function(){location.href="https://github.com/topcheer/etunnel"},5000)</script>
</main></body></html>`
}

// htmlEscapeText escapes install text for embedding in generated HTML.
func htmlEscapeText(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "\n", "\n")
	return r.Replace(s)
}
