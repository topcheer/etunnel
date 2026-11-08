// Command etunnel exposes a local port through an etunnel server.
//
//	etunnel -s wss://t.example.com/tunnel 8080
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	etunnel "etunnel/sdk"
)

func main() {
	var (
		server    = flag.String("server", os.Getenv("ETUNNEL_SERVER"), "tunnel server, e.g. wss://t.example.com/tunnel")
		local     = flag.String("local", "", "local address to expose (default: 127.0.0.1:<port arg>)")
		subdomain = flag.String("subdomain", "", "request a custom subdomain (paid plans)")
		token     = flag.String("token", os.Getenv("ETUNNEL_TOKEN"), "paid plan API key")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: etunnel [-server URL] [-token KEY] [-subdomain NAME] <local-port-or-addr>\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() < 1 && *local == "" {
		flag.Usage()
		os.Exit(2)
	}
	if *local == "" {
		arg := flag.Arg(0)
		*local = arg
		if len(arg) > 0 && arg[0] == ':' {
			*local = "127.0.0.1" + arg
		} else if allDigits(arg) {
			*local = "127.0.0.1:" + arg
		}
	}
	if *server == "" {
		fmt.Fprintln(os.Stderr, "etunnel: -server (or ETUNNEL_SERVER) is required")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	t, err := etunnel.Connect(ctx, etunnel.Options{
		Server:    *server,
		LocalAddr: *local,
		Subdomain: *subdomain,
		Token:     *token,
		Client:    "etunnel-cli/1.0",
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "etunnel:", err)
		os.Exit(1)
	}

	fmt.Printf("\n  %s  ->  http://%s\n", t.URL(), *local)
	if !t.ExpiresAt().IsZero() {
		fmt.Printf("  plan: %s, expires: %s\n", t.Plan(), t.ExpiresAt().Format(time.RFC3339))
	}
	fmt.Println()

	<-t.Done()
	fmt.Fprintln(os.Stderr, "\netunnel: closed:", t.CloseReason())
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
