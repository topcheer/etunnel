package server

// Regression test for #235: mesh Forward used to proxy upgrade requests
// through http.Client, which cannot carry a 101 both ways - cross-node
// WebSocket tunnels got a handshake with no working return path. The
// hijacked pipe must relay the 101 verbatim and carry bytes in both
// directions, including frames the visitor sent in the same packet as its
// handshake (the hijacked bufio buffer path).

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMeshForwardCarriesWebSocketUpgrade(t *testing.T) {
	t.Setenv("ETUNNEL_MESH_SECRET", "test-shared-key")
	a, aPort := newTestMesh(t, "a")
	b, _ := newTestMesh(t, "b")
	if n, err := b.ml.Join([]string{fmt.Sprintf("127.0.0.1:%d", aPort+100)}); err != nil || n == 0 {
		t.Fatalf("join: n=%d err=%v", n, err)
	}
	time.Sleep(300 * time.Millisecond)
	a.Claim("quiet-frog-42")
	time.Sleep(300 * time.Millisecond)

	// Origin: raw TCP server answering any request with 101, then echoing
	// every byte (a minimal WebSocket peer for pipe-level assertions).
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
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if strings.TrimSpace(line) == "" {
						break
					}
				}
				fmt.Fprint(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	b.mu.Lock()
	b.owner["quiet-frog-42"] = ln.Addr().String()
	b.mu.Unlock()

	// The "non-owner" node b receives the visitor request and forwards.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !b.Forward("quiet-frog-42", w, r) {
			http.Error(w, "forward failed", http.StatusBadGateway)
		}
	}))
	t.Cleanup(ts.Close)

	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(ts.URL, "http://"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Handshake plus an early payload in ONE write: exercises the hijacked
	// bufio buffer so bytes after the head are never stranded.
	handshake := "GET /ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\nEARLY-FRAME"
	if _, err := conn.Write([]byte(handshake)); err != nil {
		t.Fatal(err)
	}

	// Read the response head.
	cr := bufio.NewReader(conn)
	status, err := cr.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "101") {
		t.Fatalf("want 101, got %q", status)
	}
	sawUpgrade := false
	for {
		line, err := cr.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(line) == "" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "upgrade:") {
			sawUpgrade = true
		}
	}
	if !sawUpgrade {
		t.Fatal("101 response lost the Upgrade header")
	}

	// Bidirectional proof: the early frame (sent with the handshake) and
	// a post-handshake frame must both echo back.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len("EARLY-FRAME"))
	if _, err := io.ReadFull(cr, got); err != nil {
		t.Fatalf("early frame not relayed (visitor->owner direction broken): %v", err)
	}
	if string(got) != "EARLY-FRAME" {
		t.Fatalf("early frame %q", got)
	}
	if _, err := conn.Write([]byte("LATE-FRAME")); err != nil {
		t.Fatal(err)
	}
	got2 := make([]byte, len("LATE-FRAME"))
	if _, err := io.ReadFull(cr, got2); err != nil {
		t.Fatalf("late frame not relayed: %v", err)
	}
	if string(got2) != "LATE-FRAME" {
		t.Fatalf("late frame %q", got2)
	}
}
