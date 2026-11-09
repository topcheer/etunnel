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
	LocalAddr string
	// Subdomain requests a custom subdomain (requires a paid plan token).
	Subdomain string
	// Token is a paid plan API key.
	Token string
	// Client is a free-form client identification string.
	Client string
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

	url         string
	hostname    string
	subdomain   string
	resumeToken string // from Welcome; lets a reconnect reclaim the subdomain
	plan        string
	expiresAt   time.Time

	ctx         context.Context
	cancel      context.CancelFunc
	out         chan wsOut
	closed      chan struct{}
	closeOnce   sync.Once
	closeReason atomic.Value // string

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

// Connect establishes a reverse tunnel and returns it once the server has
// assigned a public hostname.
func Connect(ctx context.Context, opts Options) (*Tunnel, error) {
	if opts.LocalAddr == "" {
		return nil, errors.New("etunnel: LocalAddr is required")
	}
	if opts.LocalAddr[0] == ':' {
		opts.LocalAddr = "127.0.0.1" + opts.LocalAddr
	}
	server := NormalizeServerURL(opts.Server)
	if server == "" {
		server = DefaultServer
	}
	if server == "" {
		return nil, errors.New("etunnel: Server is required")
	}

	dialCtx, dialCancel := context.WithTimeout(ctx, 15*time.Second)
	ws, _, err := websocket.Dial(dialCtx, server, &websocket.DialOptions{
		Subprotocols:    []string{protocol.Subprotocol},
		CompressionMode: websocket.CompressionDisabled,
	})
	dialCancel()
	if err != nil {
		return nil, fmt.Errorf("etunnel: dial %s: %w", server, err)
	}
	ws.SetReadLimit(128 * 1024)

	tctx, tcancel := context.WithCancel(ctx)
	t := &Tunnel{
		ws:        ws,
		localAddr: opts.LocalAddr,
		ctx:       tctx,
		cancel:    tcancel,
		out:       make(chan wsOut, 256),
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

	// Handshake.
	hello, _ := protocol.EncodeControl(&protocol.Hello{
		Type:      protocol.MsgHello,
		Proto:     protocol.ProtoVersion,
		Subdomain: opts.Subdomain,
		Token:     opts.Token,
		Client:    opts.Client,
		Resume:    opts.resume,
	})
	wctx, wcancel := context.WithTimeout(tctx, 10*time.Second)
	err = ws.Write(wctx, websocket.MessageText, hello)
	wcancel()
	if err != nil {
		_ = ws.Close(websocket.StatusProtocolError, "hello failed")
		return nil, fmt.Errorf("etunnel: send hello: %w", err)
	}

	rctx, rcancel := context.WithTimeout(tctx, 15*time.Second)
	typ, rd, err := ws.Reader(rctx)
	if err == nil && typ != websocket.MessageText {
		err = errors.New("expected text welcome")
	}
	var b []byte
	if err == nil {
		b, err = io.ReadAll(io.LimitReader(rd, 64*1024))
	}
	rcancel()
	if err != nil {
		_ = ws.Close(websocket.StatusProtocolError, "no welcome")
		return nil, fmt.Errorf("etunnel: read welcome: %w", err)
	}
	msg, err := protocol.DecodeControl(b)
	if err != nil {
		_ = ws.Close(websocket.StatusProtocolError, "bad welcome")
		return nil, err
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
		_ = ws.Close(websocket.StatusPolicyViolation, m.Code)
		return nil, &APIError{Code: m.Code, Message: m.Message}
	default:
		_ = ws.Close(websocket.StatusProtocolError, "unexpected message")
		return nil, fmt.Errorf("etunnel: unexpected handshake message %T", msg)
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
func (t *Tunnel) CloseReason() string {
	if v, ok := t.closeReason.Load().(string); ok {
		return v
	}
	return ""
}

// Close shuts the tunnel down.
func (t *Tunnel) Close(reason string) {
	t.closeOnce.Do(func() {
		if reason == "" {
			reason = "client closed"
		}
		t.closeReason.Store(reason)
		// Enqueue bye before the signal so the writer flushes it first.
		bye, _ := protocol.EncodeControl(&protocol.Bye{Type: protocol.MsgBye, Reason: reason})
		select {
		case t.out <- wsOut{text: true, data: bye}:
		default:
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
	for {
		select {
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
			for {
				select {
				case m := <-t.out:
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					typ := websocket.MessageBinary
					if m.text {
						typ = websocket.MessageText
					}
					_ = t.ws.Write(ctx, typ, m.data)
					cancel()
				default:
					// Graceful close handshake, then release the reader.
					_ = t.ws.Close(websocket.StatusNormalClosure, "bye")
					t.cancel()
					return
				}
			}
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
			return
		}
		t.lastActivity.Store(time.Now().UnixNano())
		switch typ {
		case websocket.MessageText:
			b, err := io.ReadAll(io.LimitReader(rd, 64*1024))
			if err != nil {
				return
			}
			msg, err := protocol.DecodeControl(b)
			if err != nil {
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
			case *protocol.Bye:
				t.Close(m.Reason)
				return
			}
		case websocket.MessageBinary:
			f, err := protocol.ReadFrame(rd)
			if err != nil {
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
