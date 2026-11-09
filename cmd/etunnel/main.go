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

// cliConfig is the parsed command line.
type cliConfig struct {
	server     string
	local      string
	subdomain  string
	token      string
	reconnect  bool
	maxBackoff time.Duration
}

func main() {
	cfg := parseArgs()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serve(ctx, cfg)
}

// parseArgs reads flags and the positional port/address, exiting on misuse.
func parseArgs() cliConfig {
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
		*local = normalizeLocalArg(flag.Arg(0))
	}
	if *server == "" {
		fmt.Fprintln(os.Stderr, "etunnel: -server (or ETUNNEL_SERVER) is required")
		os.Exit(2)
	}
	return cliConfig{
		server:     *server,
		local:      *local,
		subdomain:  *subdomain,
		token:      *token,
		reconnect:  *reconnect,
		maxBackoff: *maxBack,
	}
}

func normalizeLocalArg(arg string) string {
	if len(arg) > 0 && arg[0] == ':' {
		return "127.0.0.1" + arg
	}
	if allDigits(arg) {
		return "127.0.0.1:" + arg
	}
	return arg
}

// serve keeps a tunnel up until the context is cancelled or the tunnel ends
// for a reason that retrying cannot fix (#15).
//
// The retry policy lives in the CLI rather than the SDK: a library that
// silently retries hides outages from its caller, and every SDK has to agree
// on that API first.
func serve(ctx context.Context, cfg cliConfig) {
	opts := etunnel.Options{
		Server:    cfg.server,
		LocalAddr: cfg.local,
		Subdomain: cfg.subdomain,
		Token:     cfg.token,
		Client:    "etunnel-cli/1.0",
	}
	attempt := 0
	for {
		t, err := etunnel.Connect(ctx, opts)
		if err != nil {
			if retryDial(ctx, cfg, err, &attempt) {
				continue
			}
			return
		}
		attempt = 0

		printBanner(t, cfg.local)

		if !awaitTunnel(ctx, t) { // false = context cancelled
			fmt.Fprintln(os.Stderr, "\netunnel: closed:", t.CloseReason())
			return
		}
		reason := t.CloseReason()
		if !cfg.reconnect || !retryableClose(reason) {
			fmt.Fprintln(os.Stderr, "\netunnel: closed:", reason)
			return
		}
		// Re-request the label we were given so the public URL stays stable;
		// a custom subdomain is already in opts.
		if opts.Subdomain == "" {
			opts.Subdomain = t.Subdomain()
		}
		attempt++
		delay := reconnectDelay(attempt, cfg.maxBackoff)
		fmt.Fprintf(os.Stderr, "etunnel: disconnected (%s); reconnecting in %s\n",
			reason, delay.Round(100*time.Millisecond))
		if !sleepCtx(ctx, delay) {
			return
		}
	}
}

// retryDial reports whether the caller should try connecting again.
func retryDial(ctx context.Context, cfg cliConfig, err error, attempt *int) bool {
	if ctx.Err() != nil {
		return false
	}
	if !cfg.reconnect {
		fmt.Fprintln(os.Stderr, "etunnel:", err)
		os.Exit(1)
	}
	*attempt++
	delay := reconnectDelay(*attempt, cfg.maxBackoff)
	fmt.Fprintf(os.Stderr, "etunnel: connect failed (%v); retrying in %s\n",
		err, delay.Round(100*time.Millisecond))
	return sleepCtx(ctx, delay)
}

// awaitTunnel blocks until the tunnel ends or the context is cancelled. It
// reports whether the tunnel ended on its own.
func awaitTunnel(ctx context.Context, t *etunnel.Tunnel) bool {
	select {
	case <-t.Done():
		return true
	case <-ctx.Done():
		t.Close("client closed")
		<-t.Done()
		return false
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

func printBanner(t *etunnel.Tunnel, local string) {
	fmt.Printf("\n  %s  ->  http://%s\n", t.URL(), local)
	if !t.ExpiresAt().IsZero() {
		fmt.Printf("  plan: %s, expires: %s\n", t.Plan(), t.ExpiresAt().Format(time.RFC3339))
	}
	fmt.Println()
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
