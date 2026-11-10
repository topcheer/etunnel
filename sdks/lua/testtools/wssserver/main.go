// wssserver is a minimal WebSocket fixture for the Lua SDK tests
// (issue #52 review must-fix): it answers the first connection with a
// welcome carrying a resumeToken, closes it, and verifies that the
// supervisor's reconnect replays the token in hello. Stdlib only.
package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strings"
)

const token = "tok-lua-test-abcdef0123456789"

func main() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("%s\n", ln.Addr()) // test script reads this line
	os.Stdout.Sync()

	conns := 0
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		conns++
		if conns > 4 {
			c.Close()
			continue
		}
		serve(c, conns == 1)
	}
}

func serve(c net.Conn, first bool) {
	defer c.Close()
	r := bufio.NewReader(c)

	req, err := readRequest(r)
	if err != nil {
		return
	}
	key := header(req, "Sec-WebSocket-Key")
	acc := func() string {
		h := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		return base64.StdEncoding.EncodeToString(h[:])
	}()
	fmt.Fprintf(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", acc)

	hello, err := readFrame(r) // client hello (masked)
	if err != nil {
		return
	}

	if first {
		welcome := fmt.Sprintf(`{"type":"welcome","url":"http://lua-test.t.test","subdomain":"lua-test","hostname":"lua-test.t.test","resumeToken":%q}`, token)
		writeFrame(c, true, []byte(welcome))
		// Abrupt close: the supervisor must reconnect.
		return
	}

	// Reconnect: the token must have survived the welcome -> supervisor -> hello hop.
	if strings.Contains(string(hello), `"resume":"`+token+`"`) {
		fmt.Println("HELLO_RESUME_OK")
	} else {
		fmt.Printf("HELLO_RESUME_MISSING hello=%q\n", string(hello))
	}
	os.Stdout.Sync()
}

func readRequest(r *bufio.Reader) (string, error) {
	var sb strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		sb.WriteString(line)
		if line == "\r\n" {
			return sb.String(), nil
		}
	}
}

func header(req, name string) string {
	for _, l := range strings.Split(req, "\r\n") {
		if strings.HasPrefix(strings.ToLower(l), strings.ToLower(name)+":") {
			return strings.TrimSpace(l[len(name)+1:])
		}
	}
	return ""
}

// readFrame reads one client (masked) text/binary frame and returns its payload.
func readFrame(r *bufio.Reader) ([]byte, error) {
	h := make([]byte, 2)
	if _, err := ioReadFull(r, h); err != nil {
		return nil, err
	}
	op := h[0] & 0x0F
	masked := h[1]&0x80 != 0
	plen := int64(h[1] & 0x7F)
	switch plen {
	case 126:
		e := make([]byte, 2)
		if _, err := ioReadFull(r, e); err != nil {
			return nil, err
		}
		plen = int64(binary.BigEndian.Uint16(e))
	case 127:
		e := make([]byte, 8)
		if _, err := ioReadFull(r, e); err != nil {
			return nil, err
		}
		plen = int64(binary.BigEndian.Uint64(e))
	}
	var mask [4]byte
	if masked {
		if _, err := ioReadFull(r, mask[:]); err != nil {
			return nil, err
		}
	}
	payload := make([]byte, plen)
	if _, err := ioReadFull(r, payload); err != nil {
		return nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	_ = op
	return payload, nil
}

func ioReadFull(r *bufio.Reader, b []byte) (int, error) {
	total := 0
	for total < len(b) {
		n, err := r.Read(b[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// writeFrame sends one server (unmasked) text frame.
func writeFrame(c net.Conn, text bool, payload []byte) {
	hdr := []byte{byte(0x80 | func() byte {
		if text {
			return 0x1
		}
		return 0x2
	}())}
	switch n := len(payload); {
	case n < 126:
		hdr = append(hdr, byte(n))
	case n < 65536:
		hdr = append(hdr, 126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 127)
		var e [8]byte
		binary.BigEndian.PutUint64(e[:], uint64(n))
		hdr = append(hdr, e[:]...)
	}
	c.Write(append(hdr, payload...))
}
