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

	// #268: after Hijack the ResponseWriter is unusable - the caller's
	// notFound/serveTunnelRequest fallback would write to a hijacked
	// writer. Every post-hijack failure therefore answers 502 on the raw
	// connection and reports handled, letting the defer close it.
	hijackFailed := func(why string) {
		body := "owner unreachable: " + why + "\n"
		_, _ = fmt.Fprintf(visitor, "HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
	}

	peer, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		hijackFailed("dial failed: " + err.Error())
		return true
	}
	defer peer.Close()

	req.Header.Set(fwdHeader, self) // loop guard survives the raw hop
	if err := req.Write(peer); err != nil {
		hijackFailed("owner handshake failed: " + err.Error())
		return true
	}

	br := bufio.NewReader(peer)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		hijackFailed("owner answer unreadable: " + err.Error())
		return true
	}
	defer resp.Body.Close()

	// Replay the response head byte-for-byte semantics: connection tokens
	// and upgrade headers must reach the visitor exactly as the owner sent
	// them, so net/http's own header handling must not re-encode them.
	fmt.Fprintf(visitor, "HTTP/1.1 %s\r\n", resp.Status)
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
	// #321 LOW4: per-op idle deadlines so a stalled side cannot pin the
	// two splice goroutines (and the session) forever.
	go func() { _ = spliceConn(peer, vbuf.Reader, visitor.SetReadDeadline, spliceIdleTimeout) }()
	_ = spliceConn(visitor, br, peer.SetReadDeadline, spliceIdleTimeout)
	return true
}

// spliceIdleTimeout bounds each read/write on an upgraded-splice connection
// (var so tests can shrink it).
var spliceIdleTimeout = 5 * time.Minute

// spliceConn copies src -> dst with per-op idle deadlines (set through the
// hook on the UNDERLYING net.Conn - src is typically a bufio.Reader wrapping
// it), so a stalled side tears the splice down instead of pinning it forever
// (#321 LOW4). src must be drained from the hijack's buffered reader so
// bytes past the handshake are not stranded.
func spliceConn(dst net.Conn, src io.Reader, setSrcReadDeadline func(time.Time) error, idle time.Duration) error {
	buf := make([]byte, 32*1024)
	for {
		_ = setSrcReadDeadline(time.Now().Add(idle))
		n, rerr := src.Read(buf)
		if n > 0 {
			_ = dst.SetWriteDeadline(time.Now().Add(idle))
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if rerr != nil {
			return rerr
		}
	}
}
