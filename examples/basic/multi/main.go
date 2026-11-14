// Multi-target demo: one public hostname serving several local services
// by URL path prefix (PROTOCOL.md "Client-side multi-target routing").
//
//	go run ./examples/basic/multi -server wss://t.etun.uk/tunnel
//
// It starts two throwaway local services (a "frontend" on :13100 and an
// "api" on :13101), opens ONE tunnel routed as:
//
//	/api -> 127.0.0.1:13101 (prefix stripped, query preserved)
//	/    -> 127.0.0.1:13100 (catch-all)
//
// then curls three URLs through the public hostname and prints what each
// local service actually received.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	etunnel "github.com/topcheer/etunnel/sdk"
)

func main() {
	server := flag.String("server", "wss://t.etun.uk/tunnel", "tunnel server")
	token := flag.String("token", "", "API key (optional)")
	flag.Parse()

	// Two local services that echo the path+query they received.
	echo := func(name string) (addr string, stop func()) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			panic(err)
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "%s saw %s?%s\n", name, r.URL.Path, r.URL.RawQuery)
		})}
		go srv.Serve(l)
		return l.Addr().String(), func() { srv.Close() }
	}
	feAddr, stopFE := echo("frontend")
	beAddr, stopBE := echo("api")
	defer stopFE()
	defer stopBE()

	tun, err := etunnel.Connect(context.Background(), etunnel.Options{
		Server: *server,
		Token:  *token,
		Client: "multi-demo/1.0",
		Routes: []etunnel.Route{
			{PathPrefix: "/api", Addr: beAddr, StripPrefix: true},
			{PathPrefix: "/", Addr: feAddr},
		},
	})
	if err != nil {
		panic(err)
	}
	defer tun.Close("demo done")

	fmt.Println("public URL:", tun.URL())
	client := &http.Client{Timeout: 10 * time.Second}
	for _, p := range []string{"/", "/app/page", "/api/users?x=1&y=2"} {
		resp, err := client.Get(tun.URL() + p)
		if err != nil {
			fmt.Println("GET", p, "error:", err)
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		fmt.Printf("GET %-20s -> %s", p, b)
	}
}
