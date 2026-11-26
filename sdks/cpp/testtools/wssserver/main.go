// Command wssserver is a test fixture for the C/C++ SDK tests: a WebSocket
// server speaking the etunnel wire protocol with a throwaway self-signed
// certificate. It exists so the SDK tests can assert that TLS verification
// actually rejects a certificate the OS does not trust (#49).
//
// Modes:
//
//	welcome    - accept, read hello, send one welcome, keep the socket open
//	drama      - welcome, then a req+FIN pair whose local target is expected to
//	             be slow, then close the socket abruptly 300ms later. This is the
//	             disconnect-while-curl-is-running scenario for the lifecycle test.
//	hellocheck - verify hello.version == ET_TEST_VERSION (HELLO_VERSION_OK /
//	             HELLO_BAD), then answer a welcome.
//	outdated   - answer hello with {"type":"error","code":"client_outdated"}
//	             (ET_TEST_OUTDATED_MSG overrides the text) and close.
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

func main() {
	mode := flag.String("mode", "welcome", "welcome|drama|hellocheck|outdated")
	addr := flag.String("listen", "127.0.0.1:0", "listen address")
	certFile := flag.String("cert", "", "serve this PEM cert instead of a throwaway self-signed one")
	keyFile := flag.String("key", "", "PEM key for -cert (required with -cert)")
	flag.Parse()

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(ln.Addr().String()) // stdout: actual address, tests read this

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"etunnel-v1"}})
			if err != nil {
				return
			}
			defer c.Close(websocket.StatusNormalClosure, "done")
			ctx := context.Background()
			_, hello, err := c.Read(ctx) // hello
			if err != nil {
				return
			}
			var h struct {
				Type    string `json:"type"`
				Version string `json:"version"`
			}
			_ = json.Unmarshal(hello, &h)
			if *mode == "hellocheck" {
				if wantV := os.Getenv("ET_TEST_VERSION"); wantV != "" && h.Version != wantV {
					fmt.Printf("HELLO_BAD: version %q != %q\n", h.Version, wantV)
					return
				}
				fmt.Printf("HELLO_VERSION_OK %s\n", h.Version)
				fmt.Printf("HELLO_RAW %s\n", string(hello)) // exactly what the client sent
			}
			if *mode == "outdated" {
				msg := os.Getenv("ET_TEST_OUTDATED_MSG")
				if msg == "" {
					msg = "etunnel client 0.0.1 is too old; this server runs 9.9.9 and requires SDK >= 1.0.0. Upgrade: https://etun.uk"
				}
				b, _ := json.Marshal(map[string]string{"type": "error", "code": "client_outdated", "message": msg})
				fmt.Printf("OUTDATED_SENT hello.version=%s\n", h.Version)
				_ = c.Write(ctx, websocket.MessageText, b)
				return
			}
			welcome := `{"type":"welcome","url":"http://fake.t.test","hostname":"fake.t.test","subdomain":"fake","resumeToken":"tok"}`
			// #338 batch 2: env-injectable welcome.proto for the SDK proto-gate tests
			// (missing/unset = no proto field = v1 tolerance path).
			if p := os.Getenv("ETUNNEL_FIXTURE_PROTO"); p != "" {
				welcome = fmt.Sprintf(`{"type":"welcome","proto":%s,`, p) + welcome[len(`{"type":"welcome",`):]
			}
			if err := c.Write(ctx, websocket.MessageText, []byte(welcome)); err != nil {
				return
			}
			if *mode == "drama" {
				// One request whose local target is slow (the test runs a
				// slow-local TCP server), then the connection dies while the
				// client's curl is still waiting.
				req := `{"type":"req","streamId":7,"method":"GET","path":"/slow"}`
				_ = c.Write(ctx, websocket.MessageText, []byte(req))
				_ = c.Write(ctx, websocket.MessageBinary, wsFrame(0x02, 7, nil)) // FIN: body done
				time.Sleep(300 * time.Millisecond)
				c.Close(websocket.StatusAbnormalClosure, "server vanished")
				return
			}
			select {} // keep the socket open until the test kills us
		}),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{certToServe(*certFile, *keyFile)}, NextProtos: []string{"http/1.1"}},
	}
	log.Fatal(srv.ServeTLS(ln, "", ""))
}

func certToServe(certFile, keyFile string) tls.Certificate {
	if certFile == "" {
		return selfSignedCert()
	}
	if keyFile == "" {
		log.Fatal("-cert requires -key")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		log.Fatal(err)
	}
	return cert
}
