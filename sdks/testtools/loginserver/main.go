// loginserver is a shared E2E fixture for the SDK CLI loopback-login flow.
// It plays the server side of /auth/cli/start|grant|exchange and, on grant,
// acts as the browser by calling the SDK's loopback /callback itself.
//
// Usage: go run ./sdks/testtools/loginserver [> addr-file]
// Prints "listening 127.0.0.1:PORT" on stdout, then one log line per request.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"sync/atomic"
)

var (
	redirect atomic.Value // string: the SDK's loopback callback URL
	devCode  = "dev1"
	oneTime  = "one-time-1"
	uriRe    = regexp.MustCompile(`^http://127\.0\.0\.1:\d+/callback$`)
)

func main() {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /auth/cli/start", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			RedirectURI string `json:"redirect_uri"`
		}
		if err := json.Unmarshal(body, &req); err != nil || !uriRe.MatchString(req.RedirectURI) {
			w.WriteHeader(400)
			fmt.Fprintln(w, "bad redirect_uri")
			fmt.Printf("START_BAD %s\n", req.RedirectURI)
			return
		}
		redirect.Store(req.RedirectURI)
		writeJSON(w, map[string]any{
			"auth_url":    "http://" + r.Host + "/auth/cli/grant?d=" + devCode,
			"device_code": devCode,
		})
		fmt.Println("START_OK")
	})

	mux.HandleFunc("GET /auth/cli/grant", func(w http.ResponseWriter, r *http.Request) {
		cb, _ := redirect.Load().(string)
		if cb == "" {
			w.WriteHeader(400)
			return
		}
		// Play the browser: land back on the SDK's loopback port with the code.
		resp, err := http.Get(cb + "?code=" + oneTime)
		if err != nil {
			fmt.Printf("GRANT_CALLBACK_ERR %v\n", err)
		} else {
			resp.Body.Close()
			fmt.Printf("GRANT_CALLBACK %d\n", resp.StatusCode)
		}
		w.WriteHeader(200)
		fmt.Fprint(w, "approve page")
	})

	mux.HandleFunc("POST /auth/cli/exchange", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			DeviceCode string `json:"device_code"`
			Code       string `json:"code"`
		}
		if err := json.Unmarshal(body, &req); err != nil || req.DeviceCode != devCode || req.Code != oneTime {
			w.WriteHeader(400)
			fmt.Fprintln(w, "bad exchange")
			fmt.Println("EXCHANGE_BAD")
			return
		}
		writeJSON(w, map[string]any{
			"api_token":  "tok-123",
			"subdomains": []string{"my-sub"},
			"plan":       "pro",
			"expires_at": int64(1730000000),
		})
		fmt.Println("EXCHANGE_OK")
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		os.Exit(1)
	}
	fmt.Println("listening", ln.Addr().String())
	if err := http.Serve(ln, mux); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
