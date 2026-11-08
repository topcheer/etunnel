// Package protocol defines the etunnel wire protocol shared between the
// server and SDK clients.
//
// Transport: one WebSocket connection per tunnel.
//   - Text frames carry JSON control messages (hello/welcome/req/res/ping/pong/bye/error).
//   - Binary frames carry multiplexed stream data with a 9-byte header.
//
// Binary frame layout (big endian):
//
//	byte     0        frame kind (0x01 DATA, 0x02 FIN, 0x03 RST)
//	uint32   1..4     stream ID
//	uint32   5..8     payload length
//	bytes    9..      payload
package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Frame kinds carried in binary WebSocket messages.
const (
	KindData byte = 0x01 // payload is a chunk of the stream body
	KindFin  byte = 0x02 // sender finished writing on this stream
	KindRst  byte = 0x03 // abort the stream
)

// HeaderSize is the size of the binary frame header.
const HeaderSize = 9

// MaxPayload is the maximum payload size of one DATA frame. Both peers must
// chunk larger writes. It also bounds the per-frame memory cost of the
// per-stream frame queues.
const MaxPayload = 32 * 1024

// ErrBadFrame indicates a malformed binary frame.
var ErrBadFrame = errors.New("etunnel: malformed frame")

// Frame is one decoded binary frame.
type Frame struct {
	Kind     byte
	StreamID uint32
	Payload  []byte
}

// Encode serializes the frame into a fresh byte slice suitable for sending as
// one binary WebSocket message.
func (f Frame) Encode() []byte {
	buf := make([]byte, HeaderSize+len(f.Payload))
	buf[0] = f.Kind
	binary.BigEndian.PutUint32(buf[1:5], f.StreamID)
	binary.BigEndian.PutUint32(buf[5:9], uint32(len(f.Payload)))
	copy(buf[HeaderSize:], f.Payload)
	return buf
}

// ReadFrame reads a single frame from a WebSocket binary message reader.
func ReadFrame(r io.Reader) (Frame, error) {
	var hdr [HeaderSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, err
	}
	f := Frame{Kind: hdr[0], StreamID: binary.BigEndian.Uint32(hdr[1:5])}
	n := binary.BigEndian.Uint32(hdr[5:9])
	if f.Kind != KindData && f.Kind != KindFin && f.Kind != KindRst {
		return Frame{}, fmt.Errorf("%w: unknown kind 0x%02x", ErrBadFrame, f.Kind)
	}
	if n > MaxPayload {
		return Frame{}, fmt.Errorf("%w: payload %d exceeds max %d", ErrBadFrame, n, MaxPayload)
	}
	if n > 0 {
		f.Payload = make([]byte, n)
		if _, err := io.ReadFull(r, f.Payload); err != nil {
			return Frame{}, err
		}
	}
	return f, nil
}
