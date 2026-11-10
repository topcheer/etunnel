// Command wssserver is the shared test fixture for the native SDK
// regression tests (#53; the same tool backs the C/C++/C# tests in their own
// PRs): a WebSocket server speaking the etunnel wire protocol with scripted
// scenarios. It is intentionally a single self-contained file so each SDK
// test dir can carry or reference it without a build step.
//
// Modes:
//
//	welcome - accept, read hello, send one welcome, hold the socket open
//	sink    - welcome, req+FIN, then read the client's res+DATA+FIN and report
//	          bytes. Crucially it sends NOTHING further inbound, so it doubles
//	          as the "silent server" for the Rust read-lock test: a client whose
//	          reader blocks outbound sends can never deliver its res here.
//	trunc   - welcome, req, then a binary DATA frame whose declared length
//	          exceeds the actual payload (truncated frame). The client must
//	          clamp instead of crashing, still answer the request, and survive.
//
// -plain serves ws:// (no TLS): tests then need no certificate dance.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"

	"github.com/coder/websocket"
)

func wsFrame(kind byte, sid uint32, payload []byte, declaredLen uint32) []byte {
	n := uint32(len(payload))
	if declaredLen > 0 {
		n = declaredLen // lie about the length: truncated-frame regression
	}
	f := make([]byte, 9+len(payload))
	f[0] = kind
	f[1] = byte(sid >> 24)
	f[2] = byte(sid >> 16)
	f[3] = byte(sid >> 8)
	f[4] = byte(sid)
	f[5] = byte(n >> 24)
	f[6] = byte(n >> 16)
	f[7] = byte(n >> 8)
	f[8] = byte(n)
	copy(f[9:], payload)
	return f
}

func handle(c *websocket.Conn, mode string) {
	ctx := context.Background()
	_, _, err := c.Read(ctx) // hello
	if err != nil {
		return
	}
	welcome := `{"type":"welcome","url":"http://fake.t.test","hostname":"fake.t.test","subdomain":"fake","resumeToken":"tok"}`
	if err := c.Write(ctx, websocket.MessageText, []byte(welcome)); err != nil {
		return
	}
	switch mode {
	case "sink":
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"req","streamId":7,"method":"GET","path":"/big"}`))
		_ = c.Write(ctx, websocket.MessageBinary, wsFrame(0x02, 7, nil, 0))
		var got int64
		var fin bool
		// no further inbound frames: the client is on its own
		for !fin {
			m, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			if m == websocket.MessageText {
				got += int64(len(data))
			} else if len(data) >= 9 && data[0] == 0x02 {
				fin = true
			} else if len(data) > 9 {
				got += int64(len(data) - 9)
			}
		}
		fmt.Printf("RES_RECEIVED %d\n", got)
		_ = c.Close(websocket.StatusNormalClosure, "done")
	case "trunc":
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"req","streamId":9,"method":"GET","path":"/t"}`))
		// declared 64KB, delivered 4 bytes
		_ = c.Write(ctx, websocket.MessageBinary, wsFrame(0x01, 9, []byte("abcd"), 65536))
		_ = c.Write(ctx, websocket.MessageBinary, wsFrame(0x02, 9, nil, 0))
		var fin bool
		for !fin {
			m, data, err := c.Read(ctx)
			if err != nil {
				fmt.Println("TRUNC_BAD: connection died before the response completed")
				return
			}
			if m == websocket.MessageText {
				continue // res header
			}
			if len(data) >= 9 && data[0] == 0x02 {
				fin = true
			}
		}
		fmt.Println("TRUNC_OK")
		_ = c.Close(websocket.StatusNormalClosure, "done")
	default: // welcome
		select {}
	}
}

func main() {
	mode := flag.String("mode", "welcome", "welcome|sink|trunc")
	addr := flag.String("listen", "127.0.0.1:0", "listen address")
	plain := flag.Bool("plain", false, "serve plain ws:// (no TLS)")
	flag.Parse()

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(ln.Addr().String()) // stdout protocol: tests read this line

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"etunnel-v1"}})
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "done")
		c.SetReadLimit(8 << 20)
		handle(c, *mode)
	})
	if *plain {
		log.Fatal(http.Serve(ln, h))
	}
	log.Fatal(http.Serve(ln, h)) // TLS variants not needed for these tests
}
