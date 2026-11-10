package etunnel_test

// Reproduces: 101-upgraded streams lose the server->external direction when
// the main listener is TLS, but work over plain HTTP. Matrix: [plain|tls].

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/topcheer/etunnel/protocol"
	"github.com/topcheer/etunnel/server"

	"github.com/coder/websocket"
)

func genCert(t *testing.T) tls.Certificate {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"localhost"},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	writePem := func(typ string, data []byte) []byte {
		var b bytes.Buffer
		fmt.Fprintf(&b, "-----BEGIN %s-----\n", typ)
		enc := base64.StdEncoding
		wrapped := enc.EncodeToString(data)
		for i := 0; i < len(wrapped); i += 64 {
			n := i + 64
			if n > len(wrapped) {
				n = len(wrapped)
			}
			b.WriteString(wrapped[i:n])
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "-----END %s-----\n", typ)
		return b.Bytes()
	}
	dir := t.TempDir()
	cf, kf := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	os.WriteFile(cf, writePem("CERTIFICATE", der), 0o600)
	os.WriteFile(kf, writePem("EC PRIVATE KEY", kb), 0o600)
	cert, err := tls.LoadX509KeyPair(cf, kf)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// fakeLocal speaks just enough HTTP to answer 101, then byte-echoes.
func fakeLocal(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				_, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				c.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"))
				io.Copy(c, c)
			}(c)
		}
	}()
	return ln.Addr().String()
}

func frameWire(kind byte, id uint32, payload []byte) []byte {
	b := make([]byte, 9+len(payload))
	b[0] = kind
	binary.BigEndian.PutUint32(b[1:5], id)
	binary.BigEndian.PutUint32(b[5:9], uint32(len(payload)))
	copy(b[9:], payload)
	return b
}

func TestTLSUpgradeRepro(t *testing.T) {
	for _, useTLS := range []bool{false, true} {
		name := "plain"
		scheme := "ws"
		if useTLS {
			name, scheme = "tls", "wss"
		}
		t.Run(name, func(t *testing.T) {
			local := fakeLocal(t)

			srv, err := server.New(server.Config{
				Domain:       "t.local",
				Addr:         "127.0.0.1:1",
				AccountsFile: filepath.Join(t.TempDir(), "a.json"),
				TLS:          server.TLSConfig{Mode: server.TLSModeHTTP},
			})
			if err != nil {
				t.Fatal(err)
			}

			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			if useTLS {
				ln = tls.NewListener(ln, &tls.Config{
					Certificates: []tls.Certificate{genCert(t)},
					NextProtos:   []string{"http/1.1"},
				})
			}
			hs := &http.Server{Handler: hostNormalizer("t.local", srv.Handler()), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 120 * time.Second} // dial-by-IP → base domain (#35)
			go hs.Serve(ln)
			t.Cleanup(func() { hs.Close() })

			addr := ln.Addr().String()
			controlURL := scheme + "://" + addr + "/tunnel"

			// --- control client (fake SDK) ---
			cli := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			ws, _, err := websocket.Dial(ctx, controlURL, &websocket.DialOptions{HTTPClient: cli, CompressionMode: websocket.CompressionDisabled})
			if err != nil {
				t.Fatal(err)
			}
			defer ws.CloseNow()
			hello, _ := json.Marshal(map[string]any{"type": "hello", "proto": 1, "client": "repro"})
			if err := ws.Write(ctx, websocket.MessageText, hello); err != nil {
				t.Fatal(err)
			}
			_, raw, err := ws.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var welcome struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(raw, &welcome); err != nil {
				t.Fatalf("welcome: %v (%s)", err, raw)
			}
			sub := strings.TrimPrefix(strings.TrimPrefix(welcome.URL, "http://"), "https://")
			t.Logf("tunnel url host: %s", sub)

			streams := map[uint32]net.Conn{}
			// pump: server ws -> local conns, and local conns -> ws
			go func() {
				for {
					typ, data, err := ws.Read(context.Background())
					if err != nil {
						return
					}
					if typ == websocket.MessageText {
						var msg map[string]any
						json.Unmarshal(data, &msg)
						switch msg["type"] {
						case "req":
							id := uint32(msg["streamId"].(float64))
							c, err := net.Dial("tcp", local)
							if err != nil {
								return
							}
							streams[id] = c
							fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: l\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: cmVwcm8=\r\nSec-WebSocket-Version: 13\r\n\r\n")
							// read 101
							br := bufio.NewReader(c)
							resp, err := http.ReadResponse(br, nil)
							if err != nil {
								return
							}
							hdrs := map[string][]string{}
							for k, v := range resp.Header {
								hdrs[k] = v
							}
							res, _ := json.Marshal(map[string]any{"type": "res", "streamId": id, "status": 101, "headers": hdrs})
							ws.Write(context.Background(), websocket.MessageText, res)
							go func(id uint32, c net.Conn, leftover []byte) {
								if len(leftover) > 0 {
									ws.Write(context.Background(), websocket.MessageBinary, frameWire(protocol.KindData, id, leftover))
								}
								buf := make([]byte, 4096)
								for {
									n, err := c.Read(buf)
									if n > 0 {
										if werr := ws.Write(context.Background(), websocket.MessageBinary, frameWire(protocol.KindData, id, buf[:n])); werr != nil {
											return
										}
									}
									if err != nil {
										ws.Write(context.Background(), websocket.MessageBinary, frameWire(protocol.KindFin, id, nil))
										return
									}
								}
							}(id, c, drain(br))
						case "ping":
							pon, _ := json.Marshal(map[string]any{"type": "pong", "ts": msg["ts"]})
							ws.Write(context.Background(), websocket.MessageText, pon)
						}
					} else {
						kind := data[0]
						id := binary.BigEndian.Uint32(data[1:5])
						n := binary.BigEndian.Uint32(data[5:9])
						payload := data[9 : 9+n]
						if c, ok := streams[id]; ok {
							if kind == protocol.KindData {
								c.Write(payload)
							} else if kind == protocol.KindFin || kind == protocol.KindRst {
								c.Close()
								delete(streams, id)
							}
						}
					}
				}
			}()

			// --- external raw client ---
			var conn net.Conn
			conn, err = net.Dial("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			if useTLS {
				conn = tls.Client(conn, &tls.Config{InsecureSkipVerify: true, ServerName: "localhost"})
			}
			defer conn.Close()
			fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: eHh4\r\nSec-WebSocket-Version: 13\r\n\r\n", sub)
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			br := bufio.NewReader(conn)
			resp, err := http.ReadResponse(br, nil)
			if err != nil {
				t.Fatalf("handshake read: %v", err)
			}
			t.Logf("external status: %d", resp.StatusCode)
			// send raw bytes, expect them back
			go func() {
				time.Sleep(100 * time.Millisecond)
				conn.Write([]byte("PING-RAW"))
			}()
			got := make([]byte, 64)
			n, err := br.Read(got)
			if err != nil {
				t.Fatalf("%s: DOWNLINK DEAD after 5s: %v", name, err)
			}
			t.Logf("%s: DOWNLINK OK: %q", name, got[:n])
		})
	}
}

func drain(br *bufio.Reader) []byte {
	var out []byte
	for br.Buffered() > 0 {
		b, err := br.ReadByte()
		if err != nil {
			break
		}
		out = append(out, b)
	}
	return out
}
