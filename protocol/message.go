package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ProtoVersion is the current protocol version.
const ProtoVersion = 1

// Subprotocol is the WebSocket subprotocol negotiated during upgrade.
const Subprotocol = "etunnel-v1"

// Control message types.
const (
	MsgHello   = "hello"
	MsgWelcome = "welcome"
	MsgReq     = "req"
	MsgRes     = "res"
	MsgPing    = "ping"
	MsgPong    = "pong"
	MsgBye     = "bye"
	MsgError   = "error"
)

// Error codes carried by error messages.
const (
	ErrCodeBadMessage     = "bad_message"
	ErrCodeProtoVersion   = "proto_version"
	ErrCodeUnauthorized   = "unauthorized"
	ErrCodeSubdomainTaken = "subdomain_taken"
	ErrCodeSubdomainPlan  = "subdomain_plan"
	ErrCodeSubdomainBad   = "subdomain_invalid"
	ErrCodeLimitReached   = "limit_reached"
	ErrCodeTTlExpired     = "ttl_expired"
	ErrCodePingTimeout    = "ping_timeout"
	ErrCodeServerShutdown = "server_shutdown"
	ErrCodeTooManyTunnels = "too_many_tunnels"
)

// Hello is the first message, client -> server.
type Hello struct {
	Type      string `json:"type"`
	Proto     int    `json:"proto"`
	Subdomain string `json:"subdomain,omitempty"` // requested custom subdomain (plan gated)
	Token     string `json:"token,omitempty"`     // paid plan API key
	Client    string `json:"client,omitempty"`    // free-form client identification
	// Resume is a server-signed token from a previous Welcome. Reconnecting
	// clients replay it to reclaim the same subdomain; it is self-validating
	// (HMAC + expiry) so it survives a server restart.
	Resume string `json:"resume,omitempty"`
}

// Welcome is the server reply to a successful Hello.
type Welcome struct {
	Type      string `json:"type"`
	Proto     int    `json:"proto"`
	Hostname  string `json:"hostname"`            // e.g. quick-fox-42.t.etun.uk
	Subdomain string `json:"subdomain"`           // e.g. quick-fox-42
	URL       string `json:"url"`                 // full public URL including scheme
	ExpiresAt int64  `json:"expiresAt,omitempty"` // unix seconds, 0 = no expiry
	Plan      string `json:"plan,omitempty"`      // tier name
	// ResumeToken lets the client reclaim this subdomain after a disconnect
	// (including a server restart). Opaque; replay it in Hello.Resume.
	ResumeToken string `json:"resumeToken,omitempty"`
}

// Req announces a new server-initiated stream carrying one HTTP request.
type Req struct {
	Type     string              `json:"type"`
	StreamID uint32              `json:"streamId"`
	Method   string              `json:"method"`
	Path     string              `json:"path"` // path + query
	Headers  map[string][]string `json:"headers,omitempty"`
}

// Res is the client's response header block for a stream.
type Res struct {
	Type     string              `json:"type"`
	StreamID uint32              `json:"streamId"`
	Status   int                 `json:"status"`
	Headers  map[string][]string `json:"headers,omitempty"`
}

// Ping / Pong are application-level heartbeats (independent of WS ping).
type Ping struct {
	Type string `json:"type"`
	TS   int64  `json:"ts"`
}

type Pong struct {
	Type string `json:"type"`
	TS   int64  `json:"ts"`
}

// Bye is sent by the side closing the connection, best effort.
type Bye struct {
	Type   string `json:"type"`
	Reason string `json:"reason,omitempty"`
}

// ErrorMsg reports a protocol or policy error.
type ErrorMsg struct {
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// DecodeControl decodes one JSON text message into a typed struct.
func DecodeControl(b []byte) (any, error) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(b, &envelope); err != nil {
		return nil, fmt.Errorf("etunnel: bad control message: %w", err)
	}
	switch envelope.Type {
	case MsgHello:
		var m Hello
		err := json.Unmarshal(b, &m)
		return &m, wrapTypeErr(err)
	case MsgWelcome:
		var m Welcome
		err := json.Unmarshal(b, &m)
		return &m, wrapTypeErr(err)
	case MsgReq:
		var m Req
		err := json.Unmarshal(b, &m)
		return &m, wrapTypeErr(err)
	case MsgRes:
		var m Res
		err := json.Unmarshal(b, &m)
		return &m, wrapTypeErr(err)
	case MsgPing:
		var m Ping
		err := json.Unmarshal(b, &m)
		return &m, wrapTypeErr(err)
	case MsgPong:
		var m Pong
		err := json.Unmarshal(b, &m)
		return &m, wrapTypeErr(err)
	case MsgBye:
		var m Bye
		err := json.Unmarshal(b, &m)
		return &m, wrapTypeErr(err)
	case MsgError:
		var m ErrorMsg
		err := json.Unmarshal(b, &m)
		return &m, wrapTypeErr(err)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownControlType, envelope.Type)
	}
}

// ErrUnknownControlType reports a well-formed control envelope whose `type`
// is not part of this protocol version. Per PROTOCOL.md both ends IGNORE
// unknown control types (forward compatibility: newer clients may probe
// extensions); callers distinguish it from malformed known messages via
// errors.Is.
var ErrUnknownControlType = errors.New("etunnel: unknown control message type")

func wrapTypeErr(err error) error {
	if err != nil {
		return fmt.Errorf("etunnel: bad control message: %w", err)
	}
	return nil
}

// EncodeControl marshals a control message to JSON bytes.
func EncodeControl(m any) ([]byte, error) {
	return json.Marshal(m)
}
