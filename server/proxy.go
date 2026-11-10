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
		}
	}()

	res, err := st.waitRes(s.cfg.HeaderWaitTimeout)
	if err != nil {
		if errors.Is(err, errStreamReset) {
			s.logf("proxy: %s %s%s: stream reset", r.Method, r.Host, r.URL.RequestURI())
			s.badGateway(w, r.Host, "tunnel disconnected")
		} else {
			s.logf("proxy: %s %s%s: no client response within %s (client stalled or gone)",
				r.Method, r.Host, r.URL.RequestURI(), s.cfg.HeaderWaitTimeout)
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

func (s *Server) badGateway(w http.ResponseWriter, host, msg string) {
	host = strings.Split(host, ":")[0]
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	_, _ = io.WriteString(w, `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>502 - upstream down</title>
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
	_, _ = io.WriteString(w, landingHTML(s.cfg))
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, notFoundHTML(s.cfg, r.Host))
}

func landingHTML(cfg Config) string {
	server := "wss://" + cfg.Domain + cfg.WSPath
	qsTabs, qsPanels := quickstartHTML(server)
	return `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="description" content="etunnel - expose any local service to the internet with one command. Instant public HTTPS URLs, automatic TLS, zero signup. 20 language SDKs.">
<link rel="icon" href="` + portalFavicon + `">
<title>etunnel - your localhost, on the internet</title>
<style>
:root{--bg:#0b0e14;--panel:#0d1117;--line:#21262d;--fg:#e6edf3;--dim:#8b949e;--green:#3fb950;--green2:#7ee787}
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:ui-monospace,'SF Mono',Menlo,Consolas,monospace;background:var(--bg);color:var(--fg);line-height:1.6}
a{color:#58a6ff;text-decoration:none}a:hover{text-decoration:underline}
/* nav */
nav{display:flex;align-items:center;justify-content:space-between;padding:1rem 2rem;border-bottom:1px solid var(--line);background:rgba(11,14,20,.85);backdrop-filter:blur(8px);position:sticky;top:0;z-index:10}
nav .logo{display:flex;align-items:center;gap:.6rem;font-weight:700;font-size:1.05rem;color:var(--fg)}
nav .links{display:flex;gap:1.5rem;font-size:.85rem}
nav .links a{color:var(--dim)}nav .links a:hover{color:var(--fg)}
/* hero */
.hero{position:relative;min-height:78vh;display:flex;align-items:center;justify-content:center;overflow:hidden;border-bottom:1px solid var(--line)}
.hero video,.hero .fallback{position:absolute;inset:0;width:100%;height:100%;object-fit:cover;opacity:.5}
.hero .fallback{background:radial-gradient(ellipse at 50% 60%,rgba(63,185,80,.12),transparent 60%),repeating-linear-gradient(90deg,transparent 0 79px,rgba(230,237,243,.03) 79px 80px)}
.hero .inner{position:relative;text-align:center;padding:4rem 1.5rem;max-width:860px}
.hero .badge{display:inline-block;border:1px solid var(--line);background:rgba(13,17,23,.7);border-radius:99px;padding:.3rem .9rem;font-size:.78rem;color:var(--dim);margin-bottom:1.4rem}
.hero h1{font-size:clamp(1.9rem,5vw,3.4rem);font-weight:800;letter-spacing:-.02em;margin-bottom:1rem}
.hero h1 .g{color:var(--green2)}
.hero p{color:var(--dim);font-size:1.02rem;max-width:34rem;margin:0 auto 2rem}
.cmd{display:flex;align-items:center;gap:.8rem;background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:.85rem 1.1rem;max-width:30rem;margin:0 auto 2rem;font-size:.95rem}
.cmd .dollar{color:var(--green2)}
.cmd .copy{margin-left:auto;color:var(--dim);cursor:pointer;font-size:.78rem;border:1px solid var(--line);border-radius:6px;padding:.15rem .5rem;background:none}
.cmd .copy:hover{color:var(--fg);border-color:var(--dim)}
.cta{display:flex;gap:1rem;justify-content:center;flex-wrap:wrap}
.btn{display:inline-block;border-radius:8px;padding:.75rem 1.5rem;font-weight:700;font-size:.95rem;border:1px solid transparent}
.btn.primary{background:var(--green);color:#0b0e14}.btn.primary:hover{background:var(--green2);text-decoration:none}
.btn.ghost{border-color:var(--line);color:var(--fg);background:rgba(13,17,23,.6)}.btn.ghost:hover{border-color:var(--dim);text-decoration:none}
/* sections */
section{max-width:1080px;margin:0 auto;padding:4.5rem 1.5rem}
section h2{font-size:1.5rem;letter-spacing:-.01em;margin-bottom:.5rem}
section .sub{color:var(--dim);margin-bottom:2.2rem;font-size:.95rem}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(250px,1fr));gap:1rem}
.card{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:1.5rem}
.card .ic{font-size:1.4rem;margin-bottom:.7rem}
.card h3{font-size:1rem;margin-bottom:.4rem}
.card p{color:var(--dim);font-size:.87rem}
/* code demo */
.demo{display:grid;grid-template-columns:1fr 1fr;gap:1rem}
@media(max-width:760px){.demo{grid-template-columns:1fr}}
pre{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:1.1rem 1.2rem;font-size:.83rem;overflow:auto;line-height:1.75}
pre .cm{color:var(--dim)}pre .k{color:#ff7b72}pre .s{color:#a5d6ff}pre .f{color:#d2a8ff}pre .g{color:var(--green2)}
pre .out{color:var(--green2)}
/* quickstart tabs */
.tabs{display:flex;gap:.4rem;flex-wrap:wrap;margin-bottom:1rem}
.tabs button.qtab{background:none;border:1px solid var(--line);color:var(--dim);border-radius:8px;padding:.35rem .75rem;font-size:.8rem;font-family:inherit;cursor:pointer}
.tabs button.qtab.on{background:var(--panel);border-color:rgba(63,185,80,.5);color:var(--fg)}
.tabs button.qtab:hover{color:var(--fg)}
.panel{display:none}
.panel.on{display:block}
/* code snippet */
.snippet{background:var(--panel);border:1px solid var(--line);border-radius:12px;overflow:hidden;max-width:52rem;margin:0 auto}
.snip-head{display:flex;align-items:center;gap:.7rem;flex-wrap:wrap;padding:.6rem .9rem;border-bottom:1px solid var(--line);background:rgba(22,27,34,.6);font-size:.78rem}
.snip-head .fname{color:var(--fg);font-weight:600}
.snip-head .mode{font-size:.64rem;text-transform:uppercase;letter-spacing:.09em;border-radius:4px;padding:.05rem .4rem;border:1px solid var(--line);color:var(--dim)}
.snip-head .mode.embed{color:var(--green2);border-color:rgba(63,185,80,.45)}
.snip-head .dep{color:var(--dim);flex:1 1 auto;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.snip-head .snip-link{margin-left:auto;color:#58a6ff;font-size:.75rem}
.snip-head .snip-copy{background:none;border:1px solid var(--line);border-radius:6px;color:var(--dim);font-family:inherit;font-size:.7rem;padding:.1rem .45rem;cursor:pointer}
.snip-head .snip-copy:hover{color:var(--fg);border-color:var(--dim)}
pre.code{margin:0;border:0;border-radius:0;background:transparent;padding:1rem 1.1rem;font-size:.8rem;line-height:1.72;overflow:auto;tab-size:4}
pre.code .kw{color:#ff7b72}
pre.code .st{color:#a5d6ff}
pre.code .cm{color:#6e7681;font-style:italic}
/* diagrams */
.flow{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:1.5rem;overflow:auto;text-align:center}
.flow pre.mermaid{background:transparent;border:0;margin:0;padding:0;font-size:.78rem;color:var(--dim);min-width:640px}
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
}
/* pricing strip */
.tiers{display:grid;grid-template-columns:repeat(auto-fit,minmax(240px,1fr));gap:1rem}
.tier{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:1.6rem}
.tier .name{font-weight:700;font-size:1.05rem}
.tier .price{font-size:1.9rem;font-weight:800;margin:.5rem 0 .3rem}
.tier .price small{font-size:.8rem;color:var(--dim);font-weight:400}
.tier ul{list-style:none;margin:.9rem 0 1.3rem;color:var(--dim);font-size:.85rem}
.tier ul li::before{content:"¹3 ";color:var(--green)}
.tier.pro{border-color:rgba(63,185,80,.5)}
/* footer */
footer{border-top:1px solid var(--line);padding:2rem;text-align:center;color:var(--dim);font-size:.8rem}
footer .sep{margin:0 .6rem}
</style></head><body>
<nav>
  <div class="logo">
    <svg width="26" height="26" viewBox="0 0 512 512"><rect width="512" height="512" rx="96" fill="#0d1117"/><rect x="120" y="128" width="272" height="256" rx="40" fill="none" stroke="#e6edf3" stroke-width="40"/><line x1="48" y1="256" x2="464" y2="256" stroke="#3fb950" stroke-width="40" stroke-linecap="round"/></svg>
    etunnel
  </div>
  <div class="links">
    <a href="#features">features</a><a href="#quickstart">quickstart</a><a href="#pricing">pricing</a><a href="https://github.com/topcheer/etunnel">github</a>
  </div>
</nav>

<div class="hero">
  <video autoplay muted loop playsinline preload="metadata">
    <source src="/assets/hero-loop.webm" type="video/webm">
    <source src="/assets/hero-loop.mp4" type="video/mp4">
  </video>
  <div class="fallback"></div>
  <div class="inner">
    <div class="badge">zero signup &middot; automatic TLS &middot; 20 language SDKs</div>
    <h1>your localhost,<br>on the <span class="g">internet</span></h1>
    <p>One command exposes any local service through NAT and firewalls to a live public HTTPS URL. No account, no config, no waiting.</p>
    <div class="cmd"><span class="dollar">$</span><span id="c">npx @easytunnel/et-sdk 3000</span><button class="copy" onclick="navigator.clipboard().writeText?navigator.clipboard.writeText(document.getElementById('c').textContent):0;this.textContent='copied'">copy</button></div>
    <div class="cta">
      <a class="btn primary" href="/pricing">Get your tunnel</a>
      <a class="btn ghost" href="https://github.com/topcheer/etunnel">Star on GitHub</a>
    </div>
  </div>
</div>

<section id="features">
  <h2>Built for developers</h2>
  <p class="sub">Everything localhost.run and friends do, without the signup wall.</p>
  <div class="grid">
    <div class="card"><div class="ic">&#128274;</div><h3>Instant HTTPS</h3><p>Wildcard TLS on every subdomain. Your webhook receivers, OAuth callbacks and payment IPNs work on the first try.</p></div>
    <div class="card"><div class="ic">&#9889;</div><h3>Zero signup</h3><p>Anonymous tunnels are free: random subdomain, 4-hour lifetime, 1 Mbps. No email, no credit card, no waitlist.</p></div>
    <div class="card"><div class="ic">&#128187;</div><h3>Any language</h3><p>20 dependency-light SDKs - Go, Node, Python, Rust, Swift, Java, C, Elixir... Embed the tunnel straight in your app.</p></div>
    <div class="card"><div class="ic">&#128200;</div><h3>WebSocket ready</h3><p>Full 101-upgrade pass-through, verified end-to-end. Real-time apps, sockets and streams just work.</p></div>
    <div class="card"><div class="ic">&#127760;</div><h3>Self-hostable</h3><p>The server is one Go binary. Point it at your domain, get your own tunnel infra in minutes.</p></div>
    <div class="card"><div class="ic">&#128179;</div><h3>Pay for more</h3><p>Custom subdomains, 30-day tunnels and 10 Mbps from $3/mo - card, PayPal or crypto.</p></div>
  </div>
</section>

<section id="how">
  <h2>How it works</h2>
  <p class="sub">One outbound WebSocket from your process. No inbound port, no router config, no firewall change.</p>
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
    <div class="card"><h3>1. Your process dials out</h3><p>The SDK opens <code>wss://&lt;domain&gt;/tunnel</code> and says <code>hello</code>. The server answers <code>welcome</code> with the public URL and the plan's TTL.</p></div>
    <div class="card"><h3>2. A request lands</h3><p>The server matches the <code>Host</code> header to your tunnel, allocates a stream id and forwards the request as JSON metadata plus binary body frames.</p></div>
    <div class="card"><h3>3. Your app answers</h3><p>The SDK replays the request against <code>localhost:3000</code> and streams the response back over the same socket. Upgrades become a raw byte pipe.</p></div>
  </div>
</section>

<section id="quickstart">
  <h2>Quickstart</h2>
  <p class="sub">The same protocol in 20 languages. Copy the snippet into your service and your app is reachable from the internet; run the file on its own and it doubles as a CLI. Free tunnels need no account.</p>
  <div class="tabs" id="langtabs">` + qsTabs + `</div>
  ` + qsPanels + `
  <p class="sub" style="text-align:center;margin:1.4rem 0 0">Custom subdomain, 30-day lifetime and 10 Mbps come with an API key - pass <code>token</code> (and <code>subdomain</code>) to the same call, see <a href="/pricing">pricing</a>.</p>
</section>

<section id="pricing">
  <h2>Pricing</h2>
  <p class="sub">Start free. Upgrade when your demo needs a name of its own.</p>
  <div class="tiers">
    <div class="tier"><div class="name">Free</div><div class="price">$0</div><ul><li>random subdomain</li><li>4-hour tunnels</li><li>1 Mbps</li><li>no account needed</li></ul><a class="btn ghost" href="#quickstart">Run it now</a></div>
    <div class="tier pro"><div class="name">Starter</div><div class="price">$3<small>/mo</small></div><ul><li>custom subdomain</li><li>72-hour tunnels</li><li>10 Mbps</li><li>API key + portal</li></ul><a class="btn primary" href="/pricing">Subscribe</a></div>
    <div class="tier"><div class="name">Pro</div><div class="price">$9<small>/mo</small></div><ul><li>5 custom subdomains</li><li>30-day tunnels</li><li>10 Mbps</li><li>priority support</li></ul><a class="btn primary" href="/pricing">Subscribe</a></div>
  </div>
</section>

<footer>
  <span>` + cfg.Domain + `</span><span class="sep">&middot;</span><a href="https://github.com/topcheer/etunnel">GitHub</a><span class="sep">&middot;</span><a href="https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md">Protocol</a><span class="sep">&middot;</span><a href="/pricing">Pricing</a><span class="sep">&middot;</span><a href="/terms">Terms</a><span class="sep">&middot;</span><a href="/privacy">Privacy</a>
  <br><span style="opacity:.6">etunnel - one binary, twenty SDKs, your localhost worldwide.</span>
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
</script>
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
mermaid.initialize({startOnLoad:true,theme:'dark',themeVariables:{fontFamily:'ui-monospace, SF Mono, Menlo, monospace',fontSize:'13px',lineColor:'#30363d',primaryColor:'#161b22',primaryTextColor:'#e6edf3',primaryBorderColor:'#3fb950',edgeLabelBackground:'#0d1117'}});
</script>
</footer>
</body></html>`
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

// htmlEscapeText escapes install text for embedding in generated HTML.
func htmlEscapeText(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "\n", "\n")
	return r.Replace(s)
}
