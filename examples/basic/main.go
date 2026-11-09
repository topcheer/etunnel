// Command basic starts a local HTTP service, exposes it through an etunnel
// server and serves until interrupted.
//
//	go run ./examples/basic                                      # public server
//	go run ./examples/basic -server wss://tunnel.internal/tunnel # your own
//	ETUNNEL_TOKEN=et_... go run ./examples/basic -subdomain myapp
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/topcheer/etunnel/sdk"
)

func main() {
	var (
		server    = flag.String("server", envOr("ETUNNEL_SERVER", etunnel.DefaultServer), "tunnel server endpoint")
		subdomain = flag.String("subdomain", os.Getenv("ETUNNEL_SUBDOMAIN"), "custom subdomain (paid plan)")
		token     = flag.String("token", os.Getenv("ETUNNEL_TOKEN"), "paid-plan API key")
		localAddr = flag.String("local", "127.0.0.1:18080", "local address to expose")
	)
	flag.Parse()

	// 1. A local service, standing in for the app you already have.
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello through the tunnel - client %s, path %s\n", r.RemoteAddr, r.URL.Path)
	})
	local := &http.Server{Addr: *localAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := local.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("local service: %v", err)
		}
	}()
	defer local.Close()

	// 2. One call publishes it. The tunnel lives inside this process: no
	//    separate daemon, no inbound port, no router configuration.
	tunnel, err := etunnel.Connect(context.Background(), etunnel.Options{
		Server:    *server,
		LocalAddr: *localAddr,
		Token:     *token,
		Subdomain: *subdomain,
		OnLog:     func(format string, args ...any) { log.Printf("[etunnel] "+format, args...) },
	})
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer tunnel.Close("example finished")

	log.Printf("local service:  http://%s/", *localAddr)
	log.Printf("public URL:     %s", tunnel.URL())
	log.Printf("plan:           %s (expires %s)", tunnel.Plan(), tunnel.ExpiresAt().Format(time.RFC3339))

	// 3. Serve until Ctrl-C, the plan TTL expires, or the server drops us.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sig:
		log.Printf("interrupted, closing tunnel")
	case <-tunnel.Done():
		log.Printf("tunnel closed by server: %s", tunnel.CloseReason())
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
