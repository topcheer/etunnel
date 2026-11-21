package etunnel_test

// #render: the Go SDK's 502 page (localErrorPage) is a full themed HTML
// document, but sendLocalError declared it text/plain - browsers showed the
// source. The Go SDK was the only family member with the bug (the TS SDK
// declares text/html). Pin the declared type against the body type for both
// SDK error pages: 502 is HTML, the route-mismatch 404 is genuinely plain
// text and keeps text/plain.

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	etunnel "github.com/topcheer/etunnel/sdk"
)

func TestSDKLocalErrorPageContentType(t *testing.T) {
	_, srvURL := startTestServer(t, nil)
	cli := clientThroughServer(t, srvURL)

	// 502: local app down -> themed HTML page, declared text/html.
	tun := connectTunnel(t, srvURL, "127.0.0.1:1", nil)
	resp, err := cli.Get(tun.URL() + "/")
	if err != nil {
		t.Fatalf("502 request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("502 status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("502 Content-Type = %q, want text/html; charset=utf-8", ct)
	}
	if !bytes.HasPrefix(body, []byte("<!doctype html>")) {
		t.Fatalf("502 body is not the themed HTML page (first 32 bytes: %q)", body[:min(32, len(body))])
	}

	// Route-mismatch 404: genuinely plain text, so text/plain is correct.
	tun2 := connectTunnel(t, srvURL, "127.0.0.1:1", func(o *etunnel.Options) {
		// A specific prefix leaves other paths unmatched -> 404 at the SDK
		// edge (the "/" catch-all would have dialed the local app instead).
		o.Routes = []etunnel.Route{{PathPrefix: "/api", Addr: "127.0.0.1:1"}}
	})
	resp2, err := cli.Get(tun2.URL() + "/nomatch")
	if err != nil {
		t.Fatalf("404 request: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("404 status = %d", resp2.StatusCode)
	}
	if ct := resp2.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("404 Content-Type = %q, want text/plain; charset=utf-8", ct)
	}
	if !bytes.HasPrefix(body2, []byte("404 page not found")) {
		t.Fatalf("404 body = %q, want the plain-text notice", body2[:min(32, len(body2))])
	}
}
