// Package etunnel is the Go SDK for etunnel tunnels: expose a local service
// on the public internet with one call.
//
//	t, err := etunnel.Connect(ctx, etunnel.Options{Server: "wss://t.example.com/tunnel", LocalAddr: ":8080"})
//	fmt.Println(t.URL()) // https://quick-fox-42.t.example.com
package etunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/topcheer/etunnel/protocol"
)

// Options configures a tunnel.
type Options struct {
	// Server is the tunnel server endpoint, e.g. "wss://t.example.com/tunnel".
	// A bare host gets "wss://" and "/tunnel" defaults.
	Server string
	// LocalAddr is the local service address to expose ("127.0.0.1:8080").
	// Ignored when Routes is set (each Route carries its own Addr).
	LocalAddr string
	// Routes serves several local services under one public hostname by
	// URL path prefix (e.g. "/" to a built SPA, "/api" to its backend).
	// Non-empty Routes switches the tunnel to multi-target mode and makes
	// LocalAddr optional. Routing is client-side; the protocol is unchanged.
	Routes []Route
	// Subdomain requests a static subdomain (a free key may hold one;
	// paid keys hold more).
	Subdomain string
	// Token is an API key (free keys unlock one static subdomain).
	Token string
	// Client is a free-form client identification string.
	Client string
	// Policy declares per-tunnel edge protection enforced by the server
	// before any visitor request reaches the local service. Nil = legacy.
	Policy *TunnelPolicy
	// OnLog, when set, receives lifecycle log lines.
	OnLog func(format string, args ...any)
	// ReconnectMinBackoff / ReconnectMaxBackoff bound the exponential backoff
	// used by ConnectReconnecting (defaults 500ms / 30s).
	ReconnectMinBackoff time.Duration
	ReconnectMaxBackoff time.Duration
	// OnReconnect, when set, fires after each successful reconnect with the
	// attempt number and the new public URL.
	OnReconnect func(attempt int, url string)

	// resume carries the previous Welcome's ResumeToken so a reconnect can
	// reclaim the same subdomain. Unexported on purpose: only
	// ConnectReconnecting sets it, callers cannot forge it.
	resume string
}

// DefaultServer is the connect target when Options.Server is empty;
// overridable at link time: -ldflags "-X etunnel/sdk.DefaultServer=wss://t.example.com/tunnel"
var DefaultServer = "wss://t.etun.uk/tunnel"

// APIError is a policy error returned by the server during handshake.
type APIError struct {
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("etunnel: %s: %s", e.Code, e.Message)
	}
	return "etunnel: " + e.Code
}

type wsOut struct {
	text bool
	data []byte
}

// Tunnel is one live reverse tunnel.
type Tunnel struct {
	ws        *websocket.Conn
	localAddr string
	// routes is non-nil only in multi-target mode (Options.Routes);
	// localAddr is then unused. See route.go.
	routes []*compiledRoute

	url         string
	hostname    string
	subdomain   string
	resumeToken string // from Welcome; lets a reconnect reclaim the subdomain
	plan        string
	expiresAt   time.Time

	ctx         context.Context
	cancel      context.CancelFunc
	out         chan wsOut
	pri         chan wsOut // #322-3: priority frames (bye) - never dropped
	closed      chan struct{}
	closeOnce   sync.Once
	closeReason atomic.Value // string
	closeErr    atomic.Value // error: terminal transport error (#266)

	mu      sync.Mutex
	streams map[uint32]*clientStream

	httpClient *http.Client
	transport  *http.Transport

	lastPong     atomic.Int64
	lastActivity atomic.Int64

	onLog func(string, ...any)
}

// NormalizeServerURL canonicalizes a server address into a ws(s) URL.
func NormalizeServerURL(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	switch {
	case strings.HasPrefix(s, "https://"):
		s = "wss://" + strings.TrimPrefix(s, "https://")
	case strings.HasPrefix(s, "http://"):
		s = "ws://" + strings.TrimPrefix(s, "http://")
	case !strings.Contains(s, "://"):
		s = "wss://" + s
	}
	if !strings.Contains(strings.TrimPrefix(strings.TrimPrefix(s, "wss://"), "ws://"), "/") {
		s += "/tunnel"
	}
	return s
}

// ErrUnsupportedProto is returned when the server speaks a strictly newer
// wire protocol than this SDK. Retrying cannot fix it; the reconnecting
// supervisor treats it as permanent.
var ErrUnsupportedProto = errors.New("etunnel: server protocol too new")

// SDK-local error sentinels (#160): the server error registry in
// PROTOCOL.md carries machine-readable codes; local failures get the
// same programmability via errors.Is. The sdk_ naming keeps them out of
// the server code namespace (proto_version, unauthorized, ...).
var (
	// ErrWelcomeTimeout: the socket opened but no welcome arrived in 15s
	// (server hung, wrong endpoint, silent proxy timeout).
	ErrWelcomeTimeout = errors.New("etunnel: sdk_timeout: no welcome within 15s")
	// ErrConnect: the WebSocket dial itself failed (DNS, TLS, refused).
	// The underlying net error stays wrapped for %w chains.
	ErrConnect = errors.New("etunnel: sdk_connect: dial failed")
	// ErrParseWelcome: the welcome frame did not decode as valid protocol.
	ErrParseWelcome = errors.New("etunnel: sdk_parse: bad welcome frame")
)

// normalizeConnectArgs validates and normalizes the endpoint arguments:
// routes vs single LocalAddr, bare-port sugar, server URL defaulting.
func normalizeConnectArgs(opts Options) (routes []*compiledRoute, server, local string, err error) {
	routes, err = normalizeRoutes(opts.Routes)
	if err != nil {
		return nil, "", "", err
	}
	if opts.LocalAddr == "" && routes == nil {
		return nil, "", "", errors.New("etunnel: LocalAddr is required")
	}
	if opts.LocalAddr != "" && opts.LocalAddr[0] == ':' {
		opts.LocalAddr = "127.0.0.1" + opts.LocalAddr
	}
	server = NormalizeServerURL(opts.Server)
	if server == "" {
		server = DefaultServer
	}
	if server == "" {
		return nil, "", "", errors.New("etunnel: Server is required")
	}
	return routes, server, opts.LocalAddr, nil
}

// dialServer opens the protocol WebSocket (ErrConnect wraps failures).
func dialServer(ctx context.Context, server string) (*websocket.Conn, error) {
	dialCtx, dialCancel := context.WithTimeout(ctx, 15*time.Second)
	ws, _, err := websocket.Dial(dialCtx, server, &websocket.DialOptions{
		Subprotocols:    []string{protocol.Subprotocol},
		CompressionMode: websocket.CompressionDisabled,
	})
	dialCancel()
	if err != nil {
		return nil, fmt.Errorf("%w: dial %s: %w", ErrConnect, server, err)
	}
	ws.SetReadLimit(128 * 1024)
	return ws, nil
}

// newTunnel assembles the Tunnel around a fresh connection; the caller
// owns handshake (which cancels the context on failure).
func newTunnel(ws *websocket.Conn, local string, routes []*compiledRoute, tctx context.Context, tcancel context.CancelFunc, opts Options) *Tunnel {
	t := &Tunnel{
		ws:        ws,
		localAddr: local,
		routes:    routes,
		ctx:       tctx,
		cancel:    tcancel,
		out:       make(chan wsOut, 256),
		pri:       make(chan wsOut, 8), // #322-3
		closed:    make(chan struct{}),
		streams:   make(map[uint32]*clientStream),
		onLog:     opts.OnLog,
		transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     false,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 0, // streams may take long before headers
		},
	}
	t.httpClient = &http.Client{Transport: t.transport}
	return t
}

func Connect(ctx context.Context, opts Options) (*Tunnel, error) {
	routes, server, local, err := normalizeConnectArgs(opts)
	if err != nil {
		return nil, err
	}
	ws, err := dialServer(ctx, server)
	if err != nil {
		return nil, err
	}
	tctx, tcancel := context.WithCancel(ctx)
	t := newTunnel(ws, local, routes, tctx, tcancel, opts)
	if err := t.handshake(opts); err != nil {
		return nil, err
	}
	go t.writeLoop()
	go t.pingLoop()
	now := time.Now().UnixNano()
	t.lastPong.Store(now)
	t.lastActivity.Store(now)
	go func() {
		t.readLoop()
	}()
	return t, nil
}

// handshake sends Hello and consumes the Welcome (or server ErrorMsg),
// filling the tunnel identity fields. On failure it cancels the tunnel
// context, closes the socket and returns the error (#162: carved out of
// Connect; behavior byte-identical).
func (t *Tunnel) handshake(opts Options) error {
	hello, _ := protocol.EncodeControl(&protocol.Hello{
		Type:      protocol.MsgHello,
		Proto:     protocol.ProtoVersion,
		Subdomain: opts.Subdomain,
		Token:     opts.Token,
		Client:    opts.Client,
		Version:   Version,
		Resume:    opts.resume,
		Policy:    policyJSON(opts.Policy),
	})
	wctx, wcancel := context.WithTimeout(t.ctx, 10*time.Second)
	err := t.ws.Write(wctx, websocket.MessageText, hello)
	wcancel()
	if err != nil {
		t.cancel()
		_ = t.ws.Close(websocket.StatusProtocolError, "hello failed")
		return fmt.Errorf("etunnel: send hello: %w", err)
	}

	rctx, rcancel := context.WithTimeout(t.ctx, 15*time.Second)
	typ, rd, err := t.ws.Reader(rctx)
	if err == nil && typ != websocket.MessageText {
		err = errors.New("expected text welcome")
	}
	var b []byte
	if err == nil {
		b, err = io.ReadAll(io.LimitReader(rd, 64*1024))
	}
	rcancel()
	if err != nil {
		t.cancel()
		_ = t.ws.Close(websocket.StatusProtocolError, "no welcome")
		// Deadline-exceeded is the common case: surface the sentinel so
		// callers can errors.Is(ErrWelcomeTimeout).
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%w: %v", ErrWelcomeTimeout, err)
		}
		return fmt.Errorf("etunnel: read welcome: %w", err)
	}
	msg, err := protocol.DecodeControl(b)
	if err != nil {
		t.cancel()
		_ = t.ws.Close(websocket.StatusProtocolError, "bad welcome")
		return fmt.Errorf("%w: %v", ErrParseWelcome, err)
	}
	// Welcome.Proto gate (#14 F#5): the server rejects hello.proto != 1;
	// the client must be symmetric - accepting any proto then decoding v2
	// traffic as v1 produces silent garbage, worse than a clean error.
	wel, _ := msg.(*protocol.Welcome)
	protoOf := func() int {
		if wel == nil {
			return -1
		}
		return wel.Proto
	}
	// The server answers a rejected hello with an ErrorMsg (APIError path),
	// which the switch below handles as-is; only a Welcome may be gated.
	// 0 (field omitted on the wire) counts as v1: the reference server
	// always sends 1, but lenient v1 peers may omit it. Only a strictly
	// newer protocol must be rejected - decoding v2 traffic as v1 yields
	// silent garbage.
	if _, isErr := msg.(*protocol.ErrorMsg); !isErr && (wel == nil || protoOf() < 0 || protoOf() > protocol.ProtoVersion) {
		t.cancel()
		_ = t.ws.Close(websocket.StatusProtocolError, "unsupported proto")
		return fmt.Errorf("%w (type=%T proto=%d, want proto %d)",
			ErrUnsupportedProto, msg, protoOf(), protocol.ProtoVersion)
	}
	switch m := msg.(type) {
	case *protocol.Welcome:
		t.url = m.URL
		t.hostname = m.Hostname
		t.subdomain = m.Subdomain
		t.resumeToken = m.ResumeToken
		t.plan = m.Plan
		if m.ExpiresAt > 0 {
			t.expiresAt = time.Unix(m.ExpiresAt, 0)
		}
	case *protocol.ErrorMsg:
		t.cancel()
		_ = t.ws.Close(websocket.StatusPolicyViolation, m.Code)
		return &APIError{Code: m.Code, Message: m.Message}
	default:
		t.cancel()
		_ = t.ws.Close(websocket.StatusProtocolError, "unexpected message")
		return fmt.Errorf("etunnel: unexpected handshake message %T", msg)
	}
	return nil
}

// URL returns the public URL of the tunnel.
func (t *Tunnel) URL() string { return t.url }

// Hostname returns the public hostname.
func (t *Tunnel) Hostname() string { return t.hostname }

// Subdomain returns the assigned subdomain label.
func (t *Tunnel) Subdomain() string { return t.subdomain }

// Plan returns the plan name reported by the server.
func (t *Tunnel) Plan() string { return t.plan }

// ExpiresAt returns the tunnel expiry reported by the server (zero = none).
func (t *Tunnel) ExpiresAt() time.Time { return t.expiresAt }

// Done is closed when the tunnel ends (server closed it, TTL expired,
// connection lost or Close was called).
func (t *Tunnel) Done() <-chan struct{} { return t.closed }

// CloseReason returns why the tunnel ended ("" while running).
//
// #266: transport-level ends no longer collapse to "read closed" - when the
// read loop recorded a terminal error, the reason names the peer close
// code/reason (server policy closes like 4403) or the transport failure.
func (t *Tunnel) CloseReason() string {
	if v, ok := t.closeReason.Load().(string); ok {
		if v == "read closed" {
			if err := t.Err(); err != nil {
				var ce *websocket.CloseError
				if errors.As(err, &ce) {
					reason := ce.Reason
					if reason == "" {
						reason = "no reason"
					}
					return fmt.Sprintf("closed by peer (code=%d %s)", ce.Code, reason)
				}
				if errors.Is(err, t.ctx.Err()) && t.ctx.Err() != nil {
					return "context canceled"
				}
				return "transport error: " + err.Error()
			}
		}
		return v
	}
	return ""
}

// Err returns the terminal error that ended the tunnel: nil while running
// or after a caller-initiated Close. WebSocket close frames surface as a
// *websocket.CloseError with Code/Reason; anything else is the transport
// failure the read loop hit (#266).
func (t *Tunnel) Err() error {
	if v, ok := t.closeErr.Load().(error); ok {
		// coder/websocket wraps close frames in its own error type without an
		// Unwrap() back to CloseError, so synthesize the CloseError from the
		// library's CloseStatus/CloseReason helpers (#266).
		if code := websocket.CloseStatus(v); code >= 0 {
			// this coder/websocket version has no CloseReason helper; the
			// close frame reason is embedded in the error text as
			// `reason = "..."` - parse it out (empty when absent).
			reason := ""
			if s := v.Error(); strings.Contains(s, `reason = "`) {
				rest := s[strings.Index(s, `reason = "`)+len(`reason = "`):]
				if j := strings.Index(rest, `"`); j >= 0 {
					reason = rest[:j]
				}
			}
			return &websocket.CloseError{Code: code, Reason: reason}
		}
		return v
	}
	return nil
}

// Close shuts the tunnel down. The goodbye frame travels on a priority
// channel (never dropped when the send queue is full) and the send loop
// drains within a bounded budget, so Close always completes promptly.
func (t *Tunnel) Close(reason string) {
	t.closeOnce.Do(func() {
		if reason == "" {
			reason = "client closed"
		}
		t.closeReason.Store(reason)
		// Enqueue bye before the signal so the writer flushes it first.
		bye, _ := protocol.EncodeControl(&protocol.Bye{Type: protocol.MsgBye, Reason: reason})
		// #322-3: bye travels on the priority channel - the ordinary queue
		// can be full (256), which used to silently drop the goodbye. Select
		// order is randomized, but the write loop drains pri unconditionally
		// and the drain budget always flushes what remains (#330-3: the
		// budget, not select order, is the guarantee).
		select {
		case t.pri <- wsOut{text: true, data: bye}:
		default:
			// #330-6 [INFO]: unreachable in practice (pri cap 8 vs one bye).
		}
		close(t.closed)
		// t.cancel() is deferred to the write loop so the reader is not
		// torn down before the bye is flushed.
		t.mu.Lock()
		streams := make([]*clientStream, 0, len(t.streams))
		for _, st := range t.streams {
			streams = append(streams, st)
		}
		t.streams = make(map[uint32]*clientStream)
		t.mu.Unlock()
		for _, st := range streams {
			st.abort()
		}
		// The write loop drains the queued bye and then closes the socket.
	})
}

func (t *Tunnel) logf(format string, args ...any) {
	if t.onLog != nil {
		t.onLog(format, args...)
	}
}

// --- outbound frames ---------------------------------------------------------

func (t *Tunnel) enqueue(text bool, data []byte) error {
	select {
	case t.out <- wsOut{text: text, data: data}:
		return nil
	case <-t.closed:
		return errClosed
	}
}

var errClosed = errors.New("etunnel: tunnel closed")

func (t *Tunnel) sendJSON(v any) error {
	b, err := protocol.EncodeControl(v)
	if err != nil {
		return err
	}
	return t.enqueue(true, b)
}

func (t *Tunnel) sendData(id uint32, chunk []byte) error {
	return t.enqueue(false, protocol.Frame{Kind: protocol.KindData, StreamID: id, Payload: chunk}.Encode())
}

func (t *Tunnel) sendFin(id uint32) error {
	return t.enqueue(false, protocol.Frame{Kind: protocol.KindFin, StreamID: id}.Encode())
}

func (t *Tunnel) sendRst(id uint32) error {
	return t.enqueue(false, protocol.Frame{Kind: protocol.KindRst, StreamID: id}.Encode())
}

func (t *Tunnel) writeLoop() {
	// Always release the tunnel context, including the write-error branch
	// below, which otherwise returns without cancelling and leaks the ctx
	// registration for the life of the parent (issue #55).
	defer t.cancel()
	for {
		select {
		case m := <-t.pri: // #322-3: priority frames bypass the ordinary queue
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			typ := websocket.MessageBinary
			if m.text {
				typ = websocket.MessageText
			}
			_ = t.ws.Write(ctx, typ, m.data)
			cancel()
		case m := <-t.out:
			// Independent timeout so the bye still flushes during shutdown.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			typ := websocket.MessageBinary
			if m.text {
				typ = websocket.MessageText
			}
			err := t.ws.Write(ctx, typ, m.data)
			cancel()
			if err != nil {
				t.Close("write failed")
				return
			}
		case <-t.closed:
			// #322-3: ONE total drain budget (was 2s per queued frame - 256
			// frames meant worst-case 8.5min); priority frames (bye) always
			// flush, ordinary frames stop at the budget.
			deadline := time.Now().Add(5 * time.Second)
			drainPri := func() {
				for {
					select {
					case m := <-t.pri:
						ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
						typ := websocket.MessageBinary
						if m.text {
							typ = websocket.MessageText
						}
						_ = t.ws.Write(ctx, typ, m.data)
						cancel()
					default:
						return
					}
				}
			}
			for time.Now().Before(deadline) {
				select {
				case m := <-t.out:
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					typ := websocket.MessageBinary
					if m.text {
						typ = websocket.MessageText
					}
					_ = t.ws.Write(ctx, typ, m.data)
					cancel()
				case <-time.After(100 * time.Millisecond):
					drainPri()
					// queue drained and quiet: finish now - waiting out the
					// whole budget would delay t.cancel() on dead sockets
					// (the #55 cancel contract, exercised by leak_test).
					if len(t.out) == 0 {
						drainPri()
						_ = t.ws.Close(websocket.StatusNormalClosure, "bye")
						t.cancel()
						return
					}
				}
			}
			drainPri()
			// Graceful close handshake, then release the reader.
			_ = t.ws.Close(websocket.StatusNormalClosure, "bye")
			t.cancel()
			return
		}
	}
}

func (t *Tunnel) pingLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := t.sendJSON(&protocol.Ping{Type: protocol.MsgPing, TS: time.Now().Unix()}); err != nil {
				return
			}
			now := time.Now().UnixNano()
			if now-t.lastPong.Load() > 150*time.Second.Nanoseconds() &&
				now-t.lastActivity.Load() > 150*time.Second.Nanoseconds() {
				t.Close("ping timeout")
				return
			}
		case <-t.closed:
			return
		}
	}
}

func (t *Tunnel) readLoop() {
	defer t.Close("read closed")
	for {
		typ, rd, err := t.ws.Reader(t.ctx)
		if err != nil {
			// #266: keep the cause - CloseReason()/Err() surface close codes
			// (policy closes, control-message-too-large) vs network faults.
			t.closeErr.Store(err)
			return
		}
		t.lastActivity.Store(time.Now().UnixNano())
		switch typ {
		case websocket.MessageText:
			b, err := io.ReadAll(io.LimitReader(rd, 64*1024))
			if err != nil {
				t.closeErr.Store(err)
				return
			}
			msg, err := protocol.DecodeControl(b)
			if err != nil {
				// #322-2 (M2): unknown control types are ignored (forward
				// compat, PROTOCOL.md §3), but a malformed KNOWN type is a
				// protocol violation and is fatal.
				if !errors.Is(err, protocol.ErrUnknownControlType) {
					t.Close("malformed control frame")
					return
				}
				continue
			}
			switch m := msg.(type) {
			case *protocol.Req:
				// Register synchronously so trailing DATA/FIN frames for this
				// stream are never dropped before serveStream starts.
				st := t.registerClientStream(m.StreamID)
				if st != nil {
					go t.serveStream(st, m)
				}
			case *protocol.Ping:
				_ = t.sendJSON(&protocol.Pong{Type: protocol.MsgPong, TS: m.TS})
			case *protocol.Pong:
				t.lastPong.Store(time.Now().UnixNano())
			case *protocol.ErrorMsg:
				// #322-5: mid-session server errors used to vanish without a
				// trace - record and end the tunnel with the real reason.
				t.Close("server error: " + m.Message)
				return
			case *protocol.Bye:
				t.Close(m.Reason)
				return
			}
		case websocket.MessageBinary:
			f, err := protocol.ReadFrame(rd)
			if err != nil {
				t.closeErr.Store(err) // #266
				return
			}
			t.mu.Lock()
			st := t.streams[f.StreamID]
			t.mu.Unlock()
			if st == nil {
				continue
			}
			select {
			case st.frames <- f:
			case <-st.done:
			default:
				// #230: this stream's local consumer stalled (its frames
				// queue is full). Never park the tunnel's only reader on
				// one stream: reset the stalled stream and keep the tunnel
				// - and every other stream on it - alive. This mirrors the
				// server-side HOL policy (PROTOCOL.md: a stream that stops
				// making progress is reset instead of stalling the tunnel).
				_ = t.sendRst(f.StreamID)
				st.abort()
			}
		}
	}
}

// Reconnect policy options. Zero values pick the defaults in reconnect.go.
// They are honoured by ConnectReconnecting only; Connect stays a one-shot
// dial so existing callers keep their exact behaviour.

// ResumeToken returns the server-signed token that lets a later connection
// reclaim this tunnel's subdomain (empty when the server does not issue one).
func (t *Tunnel) ResumeToken() string { return t.resumeToken }
