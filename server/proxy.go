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
	server := "wss://" + cfg.Domain + cfg.WSPath
	type qs struct{ ID, Name, Install string }
	qsLangs := []qs{
		{"node", "Node", "# no install needed - runs straight from npm\nnpx @easytunnel/et-sdk 3000"},
		{"ts", "TypeScript", "# source-only: grab the file, run with Node >= 22\ncurl -O https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/typescript/etunnel.ts\nnode etunnel.ts 3000"},
		{"deno", "Deno", "deno run --allow-net https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/deno/etunnel.ts 3000"},
		{"python", "Python", "pip install etunnel\netunnel 3000"},
		{"go", "Go", "go install github.com/topcheer/etunnel/cmd/etunnel@latest\netunnel -server '" + server + "' 3000"},
		{"java", "Java", "# single-file source launch (JDK 11+)\ncurl -O https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/java/src/main/java/dev/ggcode/Etunnel.java\njava Etunnel.java 3000"},
		{"kotlin", "Kotlin", "curl -O https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/kotlin/Etunnel.kt\nkotlinc Etunnel.kt -include-runtime -d etunnel.jar\njava -jar etunnel.jar 3000"},
		{"scala", "Scala", "curl -O https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/scala/Etunnel.scala\nscala Etunnel.scala 3000"},
		{"csharp", "C#", "# .NET 10 single-file app\ndotnet add package etunnel\ndotnet run 3000"},
		{"rust", "Rust", "cargo install etunnel\netunnel 3000"},
		{"swift", "Swift", "git clone https://github.com/topcheer/etunnel\ncd etunnel && swift build -c release\n.build/release/etunnel 3000"},
		{"dart", "Dart", "dart pub add etunnel\ndart run etunnel.dart 3000"},
		{"ruby", "Ruby", "gem install etunnel\netunnel 3000"},
		{"php", "PHP", "curl -O https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/php/etunnel.php\nphp etunnel.php 3000"},
		{"perl", "Perl", "curl -O https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/perl/etunnel.pl\nperl etunnel.pl 3000"},
		{"lua", "Lua", "luarocks install ggcode.etunnel\nlua $(luarocks show ggcode.etunnel | awk '/Location/{print $2}')/etunnel.lua 3000"},
		{"c", "C", "curl -O https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/c/etunnel.c\ncc -O2 -o etunnel etunnel.c -lwebsockets -lcurl -lssl -lcrypto\n./etunnel 3000"},
		{"cpp", "C++", "curl -O https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/cpp/etunnel.cpp\nc++ -std=c++17 -O2 -o etunnel etunnel.cpp -lssl -lcrypto\n./etunnel 3000"},
		{"clojure", "Clojure", "curl -O https://raw.githubusercontent.com/topcheer/etunnel/main/sdks/clojure/etunnel.clj\nclojure -M -i etunnel.clj -e \"(et/-main \\\"3000\\\")\""},
		{"elixir", "Elixir", "mix deps.get\nmix run --no-halt runner.exs 3000"},
	}
	var tb, pn strings.Builder
	for i, l := range qsLangs {
		cls := ""
		if i == 0 {
			cls = ` class="on"`
		}
		fmt.Fprintf(&tb, `<button%s onclick="showLang('%s')">%s</button>`, cls, l.ID, l.Name)
		fmt.Fprintf(&pn, `<div class="panel%s" id="p-%s"><div class="cmd" style="max-width:44rem;margin:0 auto;white-space:pre-wrap;text-align:left"><span id="c-%s">%s</span><button class="copy" onclick="navigator.clipboard.writeText(document.getElementById('c-%s').textContent);this.textContent='copied'">copy</button></div></div>`, cls, l.ID, l.ID, htmlEscapeText(l.Install), l.ID)
	}
	qsTabs, qsPanels := tb.String(), pn.String()
	return `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="description" content="etunnel - expose any local service to the internet with one command. Instant public HTTPS URLs, automatic TLS, zero signup. 20 language SDKs.">
<link rel="icon" href="data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 512 512' width='512' height='512' role='img' aria-label='etunnel'>  <rect width='512' height='512' rx='96' fill='#0d1117'/>  <rect x='120' y='128' width='272' height='256' rx='40' fill='none' stroke='#e6edf3' stroke-width='40'/>  <line x1='48' y1='256' x2='464' y2='256' stroke='#3fb950' stroke-width='40' stroke-linecap='round'/></svg>">
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
.tabs button{background:none;border:1px solid var(--line);color:var(--dim);border-radius:8px;padding:.35rem .75rem;font-size:.8rem;font-family:inherit;cursor:pointer}
.tabs button.on{background:var(--panel);border-color:rgba(63,185,80,.5);color:var(--fg)}
.tabs button:hover{color:var(--fg)}
.panel{display:none}
.panel.on{display:block}
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

<section id="quickstart">
  <h2>Quickstart - 20 languages</h2>
  <p class="sub">Pick your stack; the CLI is the same everywhere. Free tunnels need no account.</p>
  <div class="tabs" id="langtabs">` + qsTabs + `</div>
  ` + qsPanels + `
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
  <span>` + cfg.Domain + `</span><span class="sep">&middot;</span><a href="https://github.com/topcheer/etunnel">GitHub</a><span class="sep">&middot;</span><a href="https://github.com/topcheer/etunnel/blob/main/PROTOCOL.md">Protocol</a><span class="sep">&middot;</span><a href="/pricing">Pricing</a>
  <br><span style="opacity:.6">etunnel - one binary, twenty SDKs, your localhost worldwide.</span>
<script>
function showLang(id){
  document.querySelectorAll('#langtabs button').forEach(b=>b.classList.toggle('on',b.textContent===document.querySelector('[onclick="showLang(\''"+id+"\')"]')?.textContent));
  document.querySelectorAll('.panel').forEach(p=>p.classList.toggle('on',p.id==='p-'+id));
}
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
