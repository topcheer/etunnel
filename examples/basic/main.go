// Basic etunnel SDK example: expose a local HTTP service in one call.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	etunnel "github.com/topcheer/etunnel/sdk"
)

func main() {
	// 1. Start (or imagine) your local service.
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello from behind the tunnel, %s!\n", r.RemoteAddr)
	})
	local := &http.Server{Addr: "127.0.0.1:18080", Handler: mux}
	go func() { _ = local.ListenAndServe() }()
	defer local.Close()

	// 2. One call to get a public URL.
	t, err := etunnel.Connect(context.Background(), etunnel.Options{
		Server:    "wss://t.example.com/tunnel", // change me
		LocalAddr: "127.0.0.1:18080",
		// Token:     "et_...",            // paid plan: custom subdomains, more bandwidth
		// Subdomain: "my-dev-box",      // requires a paid plan
	})
	if err != nil {
		log.Fatal(err)
	}
	defer t.Close("done")

	fmt.Println("public URL:", t.URL())
	fmt.Println("plan:", t.Plan())
	time.Sleep(10 * time.Minute)
}
