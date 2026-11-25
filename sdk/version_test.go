package etunnel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The SDK must advertise a parseable semver: a release server rejects hellos
// without one (client_outdated), so a malformed sdk.Version would lock the
// SDK out of its own server.
func TestSDKVersionIsSemver(t *testing.T) {
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(Version) {
		t.Fatalf("sdk.Version %q is not major.minor.patch", Version)
	}
}

// hello carries "version": <sdk.Version>, and a server client_outdated error
// reaches the caller as an *APIError whose message keeps the download URL.
func TestHelloCarriesVersionAndOutdatedErrorSurfaces(t *testing.T) {
	var hello string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		_, raw, err := c.Read(ctx)
		if err != nil {
			return
		}
		hello = string(raw)
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"error","code":"client_outdated","message":"etunnel client 1.7.34 is too old; this server runs 1.7.40 and requires SDK >= 1.7.35. Upgrade: https://dl.example/etunnel"}`))
	}))
	defer srv.Close()

	_, err := Connect(context.Background(), Options{
		Server: "ws" + strings.TrimPrefix(srv.URL, "http") + "/tunnel", LocalAddr: "127.0.0.1:1",
	})
	if !strings.Contains(hello, `"version":"`+Version+`"`) {
		t.Fatalf("hello lacks \"version\":\"%s\": %s", Version, hello)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "client_outdated" {
		t.Fatalf("want *APIError{code:client_outdated}, got %v", err)
	}
	if !strings.Contains(err.Error(), "Upgrade: https://dl.example/etunnel") {
		t.Fatalf("outdated error lost the download URL: %v", err)
	}
}
