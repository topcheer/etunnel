// Command wssserver is a test fixture for the C/C++ SDK tests: a WebSocket
// server speaking the etunnel wire protocol, optionally with a throwaway
// self-signed certificate, with scripted scenarios for the regression tests
// (#48 / #49).
//
// Modes:
//
//	welcome    - accept, read hello, send one welcome, hold the socket open
//	drama      - welcome, then req+FIN (local target expected slow), then
//	             close abruptly 300ms later: disconnect-while-curl-running.
//	sink       - welcome, req+FIN, then read the client's res+DATA+FIN frames
//	             and report how many bytes arrived (long-header regression).
//	hellocheck - parse hello strictly; verify ET_TEST_TOKEN arrives intact
//	             and the frame is valid JSON (escaping regression).
//
// Flags: -listen (default 127.0.0.1:0), -plain (no TLS; the C client tests
// use ws:// so no certificate dance is needed - LWS verifies by default).
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/coder/websocket"
)

func selfSignedCert() tls.Certificate {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "etunnel-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		log.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func wsFrame(kind byte, sid uint32, payload []byte) []byte {
	f := make([]byte, 9+len(payload))
	f[0] = kind
	f[1] = byte(sid >> 24)
	f[2] = byte(sid >> 16)
	f[3] = byte(sid >> 8)
	f[4] = byte(sid)
	n := uint32(len(payload))
	f[5] = byte(n >> 24)
	f[6] = byte(n >> 16)
	f[7] = byte(n >> 8)
	f[8] = byte(n)
	copy(f[9:], payload)
	return f
}

func handle(c *websocket.Conn, mode string) {
	ctx := context.Background()
	mt, hello, err := c.Read(ctx) // hello
	if err != nil {
		return
	}
	if mode == "hellocheck" {
		var h struct {
			Type  string `json:"type"`
			Token string `json:"token"`
			Sub   string `json:"subdomain"`
		}
		if err := json.Unmarshal(hello, &h); err != nil {
			fmt.Printf("HELLO_BAD: not valid JSON: %v\n", err)
			return
		}
		want := os.Getenv("ET_TEST_TOKEN")
		if h.Token != want {
			fmt.Printf("HELLO_BAD: token %q != %q\n", h.Token, want)
			return
		}
		fmt.Printf("HELLO_OK %s\n", h.Token)
		return
	}
	welcome := `{"type":"welcome","url":"http://fake.t.test","hostname":"fake.t.test","subdomain":"fake","resumeToken":"tok"}`
	if err := c.Write(ctx, websocket.MessageText, []byte(welcome)); err != nil {
		return
	}
	if mode == "drama" {
		req := `{"type":"req","streamId":7,"method":"GET","path":"/slow"}`
		_ = c.Write(ctx, websocket.MessageText, []byte(req))
		_ = c.Write(ctx, websocket.MessageBinary, wsFrame(0x02, 7, nil)) // FIN: body done
		time.Sleep(300 * time.Millisecond)
		_ = c.Close(websocket.StatusAbnormalClosure, "server vanished")
		return
	}
	if mode == "sink" {
		req := `{"type":"req","streamId":7,"method":"GET","path":"/big"}`
		_ = c.Write(ctx, websocket.MessageText, []byte(req))
		_ = c.Write(ctx, websocket.MessageBinary, wsFrame(0x02, 7, nil))
		// read everything the client sends: res (text), DATA frames, FIN
		var gotRes int64
		var fin bool
		for !fin {
			mt, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			if mt == websocket.MessageText {
				gotRes += int64(len(data))
			} else if len(data) >= 9 && data[0] == 0x02 {
				fin = true
			} else {
				gotRes += int64(len(data) - 9)
			}
		}
		fmt.Printf("RES_RECEIVED %d\n", gotRes)
		_ = c.Close(websocket.StatusNormalClosure, "done")
		return
	}
	_ = mt
	select {} // welcome mode: hold the socket open until the test kills us
}

func main() {
	mode := flag.String("mode", "welcome", "welcome|drama|sink|hellocheck")
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
		c.SetReadLimit(8 << 20) // long-header regression sends ~80KB res messages
		handle(c, *mode)
	})
	if *plain {
		log.Fatal(http.Serve(ln, h))
	}
	srv := &http.Server{
		Handler:   h,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{selfSignedCert()}, NextProtos: []string{"http/1.1"}},
	}
	log.Fatal(srv.ServeTLS(ln, "", ""))
}
