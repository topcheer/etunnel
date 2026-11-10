// Command wssserver is a test fixture for the C#/C/C++ SDK tests: a
// WebSocket server speaking the etunnel wire protocol with scripted
// scenarios for the regression tests (#48/#49/#54).
//
// Modes:
//
//	welcome - accept, read hello, send one welcome, hold the socket open
//	drama   - welcome, req+FIN, close abruptly 300ms later (drop mid-request)
//	sink    - welcome, req+FIN, read the client's res+DATA+FIN, report bytes
//	hellocheck - parse hello strictly, verify ET_TEST_TOKEN arrives intact
//	burst   - welcome, then 24 concurrent reqs with distinct sids; reads every
//	          reply and enforces per-sid ordering (res before DATA before FIN),
//	          which concurrent ClientWebSocket.SendAsync calls used to violate.
//
// -plain serves ws:// (no TLS); tests then need no certificate dance.
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

type burstState struct {
	gotRes bool
	done   bool
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
		if want := os.Getenv("ET_TEST_TOKEN"); h.Token != want {
			fmt.Printf("HELLO_BAD: token %q != %q\n", h.Token, want)
			return
		}
		fmt.Printf("HELLO_OK %s\n", h.Token)
		return
	}
	_ = mt
	welcome := `{"type":"welcome","url":"http://fake.t.test","hostname":"fake.t.test","subdomain":"fake","resumeToken":"tok"}`
	if err := c.Write(ctx, websocket.MessageText, []byte(welcome)); err != nil {
		return
	}
	switch mode {
	case "drama":
		req := `{"type":"req","streamId":7,"method":"GET","path":"/slow"}`
		_ = c.Write(ctx, websocket.MessageText, []byte(req))
		_ = c.Write(ctx, websocket.MessageBinary, wsFrame(0x02, 7, nil))
		time.Sleep(300 * time.Millisecond)
		_ = c.Close(websocket.StatusAbnormalClosure, "server vanished")
	case "sink":
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"req","streamId":7,"method":"GET","path":"/big"}`))
		_ = c.Write(ctx, websocket.MessageBinary, wsFrame(0x02, 7, nil))
		var got int64
		var fin bool
		for !fin {
			m, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			if m == websocket.MessageText {
				got += int64(len(data))
			} else if len(data) >= 9 && data[0] == 0x02 {
				fin = true
			} else {
				got += int64(len(data) - 9)
			}
		}
		fmt.Printf("RES_RECEIVED %d\n", got)
		_ = c.Close(websocket.StatusNormalClosure, "done")
	case "burst":
		const n = 24
		for i := 1; i <= n; i++ {
			req := fmt.Sprintf(`{"type":"req","streamId":%d,"method":"GET","path":"/p%d"}`, i, i)
			if err := c.Write(ctx, websocket.MessageText, []byte(req)); err != nil {
				return
			}
			_ = c.Write(ctx, websocket.MessageBinary, wsFrame(0x02, uint32(i), nil))
		}
		states := make(map[uint32]*burstState, n)
		for i := 1; i <= n; i++ {
			states[uint32(i)] = &burstState{}
		}
		for done := 0; done < n; {
			m, data, err := c.Read(ctx)
			if err != nil {
				fmt.Printf("BURST_BAD: read: %v\n", err)
				return
			}
			if m == websocket.MessageText {
				var r struct {
					StreamID uint32 `json:"streamId"`
				}
				if err := json.Unmarshal(data, &r); err != nil {
					fmt.Printf("BURST_BAD: res not JSON: %v\n", err)
					return
				}
				st, ok := states[r.StreamID]
				if !ok {
					fmt.Printf("BURST_BAD: res for unknown sid %d\n", r.StreamID)
					return
				}
				if st.gotRes || st.done {
					fmt.Printf("BURST_BAD: second res for sid %d\n", r.StreamID)
					return
				}
				st.gotRes = true
				continue
			}
			if len(data) < 9 {
				fmt.Printf("BURST_BAD: short binary frame\n")
				return
			}
			sid := uint32(data[1])<<24 | uint32(data[2])<<16 | uint32(data[3])<<8 | uint32(data[4])
			st, ok := states[sid]
			if !ok {
				fmt.Printf("BURST_BAD: frame for unknown sid %d\n", sid)
				return
			}
			if !st.gotRes {
				// DATA/FIN before the res header: two streams' sends interleaved
				fmt.Printf("BURST_BAD: frame for sid %d before its res\n", sid)
				return
			}
			if data[0] == 0x02 {
				st.done = true
				done++
			}
		}
		fmt.Printf("BURST_OK %d\n", n)
		_ = c.Close(websocket.StatusNormalClosure, "done")
	default: // welcome
		select {}
	}
}

func main() {
	mode := flag.String("mode", "welcome", "welcome|drama|sink|hellocheck|burst")
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
	srv := &http.Server{
		Handler:   h,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{selfSignedCert()}, NextProtos: []string{"http/1.1"}},
	}
	log.Fatal(srv.ServeTLS(ln, "", ""))
}
