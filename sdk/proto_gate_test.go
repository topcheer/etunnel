package etunnel

// #14 F#5: the client must reject a welcome whose proto is not the one it
// speaks (symmetric with the server's hello.proto gate), instead of
// silently decoding future-protocol traffic as v1.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

func TestConnectRejectsWrongWelcomeProto(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		// future server: proto 2 welcome
		_ = c.Write(context.Background(), websocket.MessageText,
			[]byte(`{"type":"welcome","proto":2,"subdomain":"x"}`))
		<-r.Context().Done()
	}))
	defer srv.Close()

	_, err := Connect(context.Background(), Options{Server: "ws" + strings.TrimPrefix(srv.URL, "http") + "/tunnel", LocalAddr: "127.0.0.1:1"})
	if !errors.Is(err, ErrUnsupportedProto) {
		t.Fatalf("want ErrUnsupportedProto, got %v", err)
	}
}
