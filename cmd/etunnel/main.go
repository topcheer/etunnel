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

	etunnel "github.com/topcheer/etunnel/sdk"
)

func main() {
	var (
		server    = flag.String("server", os.Getenv("ETUNNEL_SERVER"), "tunnel server, e.g. wss://t.example.com/tunnel")
		local     = flag.String("local", "", "local address to expose (default: 127.0.0.1:<port arg>)")
		subdomain = flag.String("subdomain", "", "request a custom subdomain (paid plans)")
		token     = flag.String("token", os.Getenv("ETUNNEL_TOKEN"), "paid plan API key")
		reconnect = flag.Bool("reconnect", true, "reconnect automatically after a transient disconnect")
		maxBack   = flag.Duration("max-backoff", 30*time.Second, "maximum delay between reconnect attempts")
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

	// Serve, and after a transient disconnect reconnect with the same label so
	// the public URL survives a network blip or a server deploy (#15). The
	// retry policy lives in the CLI rather than the SDK: a library that
	// silently retries hides outages from its caller, and every SDK has to
	// agree on that API first.
	opts := etunnel.Options{
		Server:    *server,
		LocalAddr: *local,
		Subdomain: *subdomain,
		Token:     *token,
		Client:    "etunnel-cli/1.0",
	}
	attempt := 0
	for {
		t, err := etunnel.Connect(ctx, opts)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if !*reconnect {
				fmt.Fprintln(os.Stderr, "etunnel:", err)
				os.Exit(1)
			}
			attempt++
			delay := reconnectDelay(attempt, *maxBack)
			fmt.Fprintf(os.Stderr, "etunnel: connect failed (%v); retrying in %s\n", err, delay.Round(100*time.Millisecond))
			select {
			case <-time.After(delay):
				continue
			case <-ctx.Done():
				return
			}
		}
		attempt = 0

		fmt.Printf("\n  %s  ->  http://%s\n", t.URL(), *local)
		if !t.ExpiresAt().IsZero() {
			fmt.Printf("  plan: %s, expires: %s\n", t.Plan(), t.ExpiresAt().Format(time.RFC3339))
		}
		fmt.Println()

		select {
		case <-t.Done():
		case <-ctx.Done():
			t.Close("client closed")
			<-t.Done()
		}
		reason := t.CloseReason()
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "\netunnel: closed:", reason)
			return
		}
		if !*reconnect || !retryableClose(reason) {
			fmt.Fprintln(os.Stderr, "\netunnel: closed:", reason)
			return
		}
		// Re-request the label we were given so the public URL stays stable;
		// a custom subdomain is already in opts.
		if opts.Subdomain == "" {
			opts.Subdomain = t.Subdomain()
		}
		attempt++
		delay := reconnectDelay(attempt, *maxBack)
		fmt.Fprintf(os.Stderr, "etunnel: disconnected (%s); reconnecting in %s\n", reason, delay.Round(100*time.Millisecond))
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
	}
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
