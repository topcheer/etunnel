package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/time/rate"

	"github.com/topcheer/etunnel/protocol"
)

var (
	errSessionClosed = errors.New("etunnel: session closed")
	errStreamReset   = errors.New("etunnel: stream reset by peer")
)

type wsOut struct {
	text bool
	data []byte
}

// Session is one live tunnel: a single WebSocket connection from an SDK
// client, owning a set of multiplexed HTTP streams.
type Session struct {
	srv    *Server
	ws     *websocket.Conn
	ip     string
	remote string
	id     string

	sub      string
	hostname string
	url      string
	custom   bool

	tier    *Tier
	account *Account
	expires time.Time // zero = no expiry
	limiter *rate.Limiter

	ctx       context.Context
	cancel    context.CancelFunc
	out       chan wsOut
	closed    chan struct{}
	closeOnce sync.Once
	closeMsg  atomic.Value // string

	mu      sync.Mutex
	streams map[uint32]*Stream
	nextID  uint32

	lastPong     atomic.Int64 // unix nanos
	lastActivity atomic.Int64
}

// Stream is one multiplexed HTTP exchange inside a session. The server sends
// the Req meta message, then DATA/FIN frames carrying the request body; the
// client answers with Res meta then DATA/FIN frames carrying the response
// body (or switches to a raw byte pipe after a 101 upgrade).
type Stream struct {
	id   uint32
	sess *Session

	ctx    context.Context
	cancel context.CancelFunc

	resCh chan *protocol.Res

	frames chan protocol.Frame
	bodyPr *io.PipeReader // response body arriving from the client
	bodyPw *io.PipeWriter

	done     chan struct{}
	doneOnce sync.Once
}

// --- accessors -------------------------------------------------------------

// Subdomain returns the assigned subdomain label.
func (s *Session) Subdomain() string { return s.sub }

// Hostname returns the full tunnel hostname (sub + base domain).
func (s *Session) Hostname() string { return s.hostname }

// URL returns the public URL of the tunnel.
func (s *Session) URL() string { return s.url }

// Plan returns the tier name.
func (s *Session) Plan() string { return s.tier.Name }

// ExpiresAt returns the tunnel expiry (zero when unlimited).
func (s *Session) ExpiresAt() time.Time { return s.expires }

// RemoteIP returns the SDK client IP.
func (s *Session) RemoteIP() string { return s.ip }

// IsCustom reports whether the tunnel holds a plan-gated custom subdomain.
func (s *Session) IsCustom() bool { return s.custom }

func (s *Session) streamCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.streams)
}

// --- websocket session entry ----------------------------------------------

// handleTunnelWS upgrades, authenticates, assigns a subdomain and runs the
// session until it ends.
func (s *Server) handleTunnelWS(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:   []string{protocol.Subprotocol},
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		return
	}
	ws.SetReadLimit(128 * 1024)

	ip, _, _ := net.SplitHostPort(r.RemoteAddr)

	// Read hello.
	helloCtx, helloCancel := context.WithTimeout(context.Background(), 15*time.Second)
	typ, rd, err := ws.Reader(helloCtx)
	if err != nil {
		helloCancel()
		_ = ws.Close(websocket.StatusProtocolError, "expected hello")
		return
	}
	if typ != websocket.MessageText {
		helloCancel()
		_ = ws.Close(websocket.StatusProtocolError, "expected text hello")
		return
	}
	b, err := io.ReadAll(io.LimitReader(rd, 64*1024))
	helloCancel()
	if err != nil {
		_ = ws.Close(websocket.StatusProtocolError, "bad hello")
		return
	}
	msg, err := protocol.DecodeControl(b)
	if err != nil {
		s.wsErrorClose(ws, protocol.ErrCodeBadMessage, err.Error())
		return
	}
	hello, ok := msg.(*protocol.Hello)
	if !ok {
		s.wsErrorClose(ws, protocol.ErrCodeBadMessage, "expected hello")
		return
	}
	if hello.Proto != protocol.ProtoVersion {
		s.wsErrorClose(ws, protocol.ErrCodeProtoVersion,
			fmt.Sprintf("server speaks proto %d, client sent %d", protocol.ProtoVersion, hello.Proto))
		return
	}

	// Resolve account + tier. The handshake reads a private snapshot: the live
	// *Account is mutated by billing/OAuth under the store lock, and this
	// goroutine (plus the stats handler via sess.account) must not observe
	// those writes unsynchronised (issue #4).
	var acct *Account
	if hello.Token != "" {
		snap, found := s.accounts.Snapshot(hello.Token)
		if !found {
			s.wsErrorClose(ws, protocol.ErrCodeUnauthorized, "unknown API token")
			return
		}
		acct = &snap
	} else if s.cfg.RequireToken {
		s.wsErrorClose(ws, protocol.ErrCodeUnauthorized, "this server requires an API token")
		return
	}
	tierID := s.cfg.DefaultTier
	if acct != nil {
		tierID = acct.Tier
	}
	tier, ok := s.cfg.Tiers[tierID]
	if !ok {
		s.wsErrorClose(ws, protocol.ErrCodeUnauthorized, "account tier no longer exists")
		return
	}

	// Per-IP limit for anonymous tunnels.
	if acct == nil && s.cfg.MaxTunnelsPerIP > 0 {
		n := 0
		for _, sess := range s.reg.Snapshot() {
			if sess.ip == ip && sess.account == nil {
				n++
			}
		}
		if n >= s.cfg.MaxTunnelsPerIP {
			s.wsErrorClose(ws, protocol.ErrCodeTooManyTunnels,
				fmt.Sprintf("too many tunnels from %s", ip))
			return
		}
	}

	// Build the session before acquiring a subdomain (registry needs it).
	now := time.Now()
	sid := make([]byte, 8)
	_, _ = rand.Read(sid)
	ctx, cancel := context.WithCancel(r.Context())
	sess := &Session{
		srv:     s,
		ws:      ws,
		ip:      ip,
		remote:  r.RemoteAddr,
		id:      hex.EncodeToString(sid),
		tier:    tier,
		account: acct,
		ctx:     ctx,
		cancel:  cancel,
		out:     make(chan wsOut, 256),
		closed:  make(chan struct{}),
		streams: make(map[uint32]*Stream),
		nextID:  1,
	}
	if tier.Bandwidth > 0 {
		sess.limiter = rate.NewLimiter(rate.Limit(tier.Bandwidth/8), protocol.MaxPayload)
	}

	// Resolve subdomain.
	if hello.Subdomain != "" {
		if acct == nil || tier.CustomDomains <= 0 {
			cancel()
			s.wsErrorClose(ws, protocol.ErrCodeSubdomainPlan,
				"custom subdomains require a paid plan")
			return
		}
		if used := s.reg.countCustomForAccount(acct); used >= tier.CustomDomains {
			cancel()
			s.wsErrorClose(ws, protocol.ErrCodeLimitReached,
				fmt.Sprintf("plan %s allows %d custom subdomain(s), all in use", tier.Name, tier.CustomDomains))
			return
		}
		sub := protocol.NormalizeSubdomain(hello.Subdomain)
		// Ownership gate: paid users may only claim subdomains they added
		// via the portal (/me) - purchase alone does not unlock any name.
		if !containsStr(acct.Domains, sub) {
			cancel()
			s.wsErrorClose(ws, protocol.ErrCodeSubdomainPlan,
				fmt.Sprintf("subdomain %q is not in your account's domain list (add it at /me first)", sub))
			return
		}
		if err := protocol.ValidateSubdomain(sub); err != nil {
			cancel()
			s.wsErrorClose(ws, protocol.ErrCodeSubdomainBad, err.Error())
			return
		}
		if err := s.reg.Acquire(sub, sess); err != nil {
			cancel()
			s.wsErrorClose(ws, protocol.ErrCodeSubdomainTaken,
				fmt.Sprintf("subdomain %q is already in use", sub))
			return
		}
		sess.sub = sub
		sess.custom = true
	} else {
		var sub string
		for i := 0; i < 12; i++ {
			cand := protocol.RandomSubdomain()
			if err := s.reg.Acquire(cand, sess); err == nil {
				sub = cand
				break
			}
		}
		if sub == "" {
			cancel()
			s.wsErrorClose(ws, protocol.ErrCodeTooManyTunnels, "no free subdomains, try again")
			return
		}
		sess.sub = sub
	}

	sess.hostname = sess.sub + "." + s.cfg.Domain
	sess.url = s.cfg.PublicScheme + "://" + sess.hostname
	if tier.TTL > 0 {
		sess.expires = now.Add(tier.TTL)
	}
	s.metrics.tunnelsTotal.Add(1)

	// Start the session machinery and answer with welcome.
	go sess.writeLoop()
	if err := sess.sendJSON(&protocol.Welcome{
		Type:      protocol.MsgWelcome,
		Proto:     protocol.ProtoVersion,
		Hostname:  sess.hostname,
		Subdomain: sess.sub,
		URL:       sess.url,
		ExpiresAt: expiresUnix(sess.expires),
		Plan:      tier.Name,
	}); err != nil {
		sess.Close("welcome failed")
		return
	}
	if tier.TTL > 0 {
		time.AfterFunc(time.Until(sess.expires), func() {
			sess.Close(protocol.ErrCodeTTlExpired)
		})
	}
	go sess.pingLoop()
	nowNano := time.Now().UnixNano()
	sess.lastPong.Store(nowNano)
	sess.lastActivity.Store(nowNano)

	// Serve until the connection dies.
	sess.readLoop()
}

func expiresUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func (s *Server) wsErrorClose(ws *websocket.Conn, code, msg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b, _ := json.Marshal(&protocol.ErrorMsg{Type: protocol.MsgError, Code: code, Message: msg})
	_ = ws.Write(ctx, websocket.MessageText, b)
	_ = ws.Close(websocket.StatusPolicyViolation, code)
}

// --- session plumbing ------------------------------------------------------

func (s *Session) enqueue(text bool, data []byte) error {
	select {
	case s.out <- wsOut{text: text, data: data}:
		return nil
	case <-s.closed:
		return errSessionClosed
	}
}

func (s *Session) sendJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.enqueue(true, b)
}

func (s *Session) sendData(id uint32, chunk []byte) error {
	return s.enqueue(false, protocol.Frame{Kind: protocol.KindData, StreamID: id, Payload: chunk}.Encode())
}

func (s *Session) sendFin(id uint32) error {
	return s.enqueue(false, protocol.Frame{Kind: protocol.KindFin, StreamID: id}.Encode())
}

func (s *Session) sendRst(id uint32) error {
	return s.enqueue(false, protocol.Frame{Kind: protocol.KindRst, StreamID: id}.Encode())
}

func (s *Session) writeLoop() {
	for {
		select {
		case m := <-s.out:
			// Independent timeout: during shutdown s.ctx is already canceled
			// but queued frames (the bye) still need to be flushed.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			typ := websocket.MessageBinary
			if m.text {
				typ = websocket.MessageText
			}
			err := s.ws.Write(ctx, typ, m.data)
			cancel()
			if err != nil {
				s.Close("write failed")
				return
			}
		case <-s.closed:
			// Flush anything already queued (e.g. the bye message) then stop.
			for {
				select {
				case m := <-s.out:
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					typ := websocket.MessageBinary
					if m.text {
						typ = websocket.MessageText
					}
					_ = s.ws.Write(ctx, typ, m.data)
					cancel()
				default:
					// Graceful close handshake first, then release the reader
					// (canceling earlier would tear the connection down before
					// the bye hits the wire).
					_ = s.ws.Close(websocket.StatusNormalClosure, "bye")
					s.cancel()
					return
				}
			}
		}
	}
}

func (s *Session) pingLoop() {
	ticker := time.NewTicker(s.srv.cfg.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := s.sendJSON(&protocol.Ping{Type: protocol.MsgPing, TS: time.Now().Unix()}); err != nil {
				return
			}
			now := time.Now().UnixNano()
			if now-s.lastPong.Load() > s.srv.cfg.PingTimeout.Nanoseconds() &&
				now-s.lastActivity.Load() > s.srv.cfg.PingTimeout.Nanoseconds() {
				s.Close(protocol.ErrCodePingTimeout)
				return
			}
		case <-s.closed:
			return
		}
	}
}

func (s *Session) readLoop() {
	defer s.Close("read closed")
	for {
		typ, rd, err := s.ws.Reader(s.ctx)
		if err != nil {
			return
		}
		s.lastActivity.Store(time.Now().UnixNano())
		switch typ {
		case websocket.MessageText:
			b, err := io.ReadAll(io.LimitReader(rd, 64*1024))
			if err != nil {
				return
			}
			// Tolerate client WebSocket libraries that send binary frames
			// with the text opcode: JSON control messages start with '{',
			// stream frames start with a kind byte.
			if len(b) > 0 && b[0] != '{' {
				if f, ferr := protocol.ReadFrame(bytes.NewReader(b)); ferr == nil {
					s.dispatchFrame(f)
					continue
				}
			}
			if err := s.handleControl(b); err != nil {
				return
			}
		case websocket.MessageBinary:
			f, err := protocol.ReadFrame(rd)
			if err != nil {
				return
			}
			s.dispatchFrame(f)
		}
	}
}

func (s *Session) handleControl(b []byte) error {
	msg, err := protocol.DecodeControl(b)
	if err != nil {
		return err
	}
	switch m := msg.(type) {
	case *protocol.Pong:
		s.lastPong.Store(time.Now().UnixNano())
	case *protocol.Ping:
		_ = s.sendJSON(&protocol.Pong{Type: protocol.MsgPong, TS: m.TS})
	case *protocol.Res:
		s.mu.Lock()
		st := s.streams[m.StreamID]
		s.mu.Unlock()
		if st != nil {
			select {
			case st.resCh <- m:
			default:
			}
		}
	case *protocol.Bye:
		return errSessionClosed
	default:
		// hello/welcome/req/error from a client are ignored
	}
	return nil
}

func (s *Session) dispatchFrame(f protocol.Frame) {
	s.mu.Lock()
	st := s.streams[f.StreamID]
	s.mu.Unlock()
	if st == nil {
		return // unknown or already finished stream: drop
	}
	select {
	case st.frames <- f:
	case <-st.done:
	}
}

// Close terminates the session: flush streams, free the subdomain.
func (s *Session) Close(reason string) {
	s.closeOnce.Do(func() {
		s.closeMsg.Store(reason)
		// Enqueue the bye BEFORE closing the signal so the writer always
		// flushes it before exiting.
		b, _ := json.Marshal(&protocol.Bye{Type: protocol.MsgBye, Reason: reason})
		select {
		case s.out <- wsOut{text: true, data: b}:
		default:
		}
		close(s.closed)
		// NOTE: s.cancel() is deferred to the write loop (after the bye is
		// flushed and the close handshake done) so a canceled reader cannot
		// race the final writes.
		s.mu.Lock()
		streams := make([]*Stream, 0, len(s.streams))
		for _, st := range s.streams {
			streams = append(streams, st)
		}
		s.streams = make(map[uint32]*Stream)
		s.mu.Unlock()
		for _, st := range streams {
			st.abort()
		}
		// The write loop drains the queued bye and then closes the socket,
		// guaranteeing the bye is sent before the close frame.
		s.srv.reg.Release(s.sub, s)
	})
}

// CloseReason returns why the session ended ("" while running).
func (s *Session) CloseReason() string {
	if v, ok := s.closeMsg.Load().(string); ok {
		return v
	}
	return ""
}

// --- streams ---------------------------------------------------------------

// OpenStream registers a new server-initiated stream, or nil if the session is
// closing or over its stream limit.
func (s *Session) OpenStream() *Stream {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.closed:
		return nil
	default:
	}
	if len(s.streams) >= s.srv.cfg.MaxStreamsPerTunnel {
		return nil
	}
	id := s.nextID
	s.nextID += 2 // server-side IDs are odd
	ctx, cancel := context.WithCancel(s.ctx)
	pr, pw := io.Pipe()
	st := &Stream{
		id:     id,
		sess:   s,
		ctx:    ctx,
		cancel: cancel,
		resCh:  make(chan *protocol.Res, 1),
		frames: make(chan protocol.Frame, 16),
		bodyPr: pr,
		bodyPw: pw,
		done:   make(chan struct{}),
	}
	s.streams[id] = st
	go st.pump()
	return st
}

func (s *Session) removeStream(id uint32) {
	s.mu.Lock()
	delete(s.streams, id)
	s.mu.Unlock()
}

func (st *Stream) finished() bool {
	select {
	case <-st.done:
		return true
	default:
		return false
	}
}

// finish marks the stream complete and removes it from the session. It does
// NOT cancel the stream context, so a response body already fully buffered
// still flushes to the external client.
func (st *Stream) finish() {
	st.doneOnce.Do(func() {
		close(st.done)
		st.sess.removeStream(st.id)
	})
}

// abort finishes the stream and cancels its context, stopping any pending
// body transfer.
func (st *Stream) abort() {
	st.finish()
	st.cancel()
	_ = st.bodyPw.CloseWithError(errStreamReset)
}

// pump moves response frames from the session dispatcher into the response
// body pipe (with backpressure), on a per-stream goroutine so a slow external
// consumer cannot stall the shared WebSocket reader.
func (st *Stream) pump() {
	for {
		select {
		case f := <-st.frames:
			switch f.Kind {
			case protocol.KindData:
				if _, err := st.bodyPw.Write(f.Payload); err != nil {
					// External side went away.
					_ = st.sess.sendRst(st.id)
					st.finish()
					return
				}
			case protocol.KindFin:
				_ = st.bodyPw.CloseWithError(io.EOF)
				st.finish()
				return
			case protocol.KindRst:
				_ = st.bodyPw.CloseWithError(errStreamReset)
				st.finish()
				return
			}
		case <-st.done:
			return
		}
	}
}

// waitRes blocks for the client's response headers.
func (st *Stream) waitRes(timeout time.Duration) (*protocol.Res, error) {
	select {
	case res := <-st.resCh:
		return res, nil
	case <-st.done:
		return nil, errStreamReset
	case <-time.After(timeout):
		return nil, errors.New("etunnel: header wait timeout")
	}
}
