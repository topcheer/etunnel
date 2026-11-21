package server

// Raw bidirectional forwarding for protocol-upgrade requests across cluster
// routers (#235). http.Client cannot carry an upgrade: after the 101 it only
// exposes the owner-side stream as a readable body, so the visitor's frames
// would never travel and the handshake would surface as a broken one-way
// pipe. The router therefore hijacks the visitor connection, dials the owner
// node over raw TCP, replays the request head, replays the response head
// verbatim (101 semantics preserved), and splices both directions.

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// forwardUpgraded proxies one upgrade request to target over raw TCP with
// both ends hijacked. It reports whether the exchange was handled (true even
// on mid-stream errors - the response head has already been committed).
func forwardUpgraded(target string, w http.ResponseWriter, req *http.Request, fwdHeader, self string) bool {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return false
	}
	visitor, vbuf, err := hj.Hijack()
	if err != nil {
		return false
	}
	defer visitor.Close()
	_ = vbuf.Writer.Flush() // nothing should be pending, but never lose bytes

	peer, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		// #268: the visitor connection is already hijacked - a bare return
		// makes the caller write to a hijacked ResponseWriter (rejected by
		// net/http) and the visitor sees a bare TCP close. Answer 502 here.
		fmt.Fprintf(visitor, "HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/plain\r\nConnection: close\r\n\r\nowner node unreachable for upgrade\n")
		return true
	}
	defer peer.Close()

	req.Header.Set(fwdHeader, self) // loop guard survives the raw hop
	if err := req.Write(peer); err != nil {
		// #268: post-hijack failure - answer 502 on the visitor conn.
		fmt.Fprintf(visitor, "HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/plain\r\nConnection: close\r\n\r\nupgrade forward failed\n")
		return true
	}

	br := bufio.NewReader(peer)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		// #268: post-hijack failure - answer 502 on the visitor conn.
		fmt.Fprintf(visitor, "HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/plain\r\nConnection: close\r\n\r\nupgrade forward failed\n")
		return true
	}
	defer resp.Body.Close()

	// Replay the response head byte-for-byte semantics: connection tokens
	// and upgrade headers must reach the visitor exactly as the owner sent
	// them, so net/http's own header handling must not re-encode them.
	fmt.Fprintf(visitor, "HTTP/1.1 %s\r\n", resp.Status)
	// #268: http.ReadResponse strips the framing headers (Content-Length /
	// Transfer-Encoding) from resp.Header. Recompute Content-Length when the
	// owner declared a known length; otherwise the body stays
	// close-delimited (deliberate: the visitor connection is hijacked, so
	// connection close bounds the body).
	if resp.ContentLength > 0 {
		resp.Header.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	if err := resp.Header.Write(visitor); err != nil {
		return true
	}
	if _, err := visitor.Write([]byte("\r\n")); err != nil {
		return true
	}

	if resp.StatusCode != http.StatusSwitchingProtocols {
		// The owner answered a normal response to an upgrade attempt;
		// replay the body (Content-Length / chunked framing was copied
		// verbatim by the head replay above).
		_, _ = io.Copy(visitor, resp.Body)
		return true
	}

	// 101: splice both directions. vbuf.Reader first so any bytes the
	// visitor sent right after its handshake (often the same packet) are
	// not stranded in the hijacked buffer.
	go func() { _, _ = io.Copy(peer, vbuf.Reader) }()
	_, _ = io.Copy(visitor, br)
	return true
}
