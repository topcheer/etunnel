package etunnel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestLoginE2E runs the full loopback flow against a real server handler:
// start -> (simulated browser approval) -> loopback callback -> exchange.
func TestLoginE2E(t *testing.T) {
	// Build a server mux with just the three CLI endpoints backed by a fake
	// exchange, mirroring the wire contract pinned by server/auth_cli_test.go.
	var lbPort string
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/cli/start", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			RedirectURI string `json:"redirect_uri"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		// The SDK's listener port is embedded in the redirect_uri it sends.
		lbPort = strings.TrimSuffix(strings.TrimPrefix(req.RedirectURI, "http://127.0.0.1:"), "/callback")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"auth_url":    serverBase + "/auth/cli/grant?dc=dev-token",
			"device_code": "dev-token",
		})
	})
	mux.HandleFunc("/auth/cli/grant", func(w http.ResponseWriter, r *http.Request) {
		// Simulated browser: 303 to the SDK's loopback listener.
		http.Redirect(w, r, "http://127.0.0.1:"+lbPort+"/callback?code=code-token", http.StatusSeeOther)
	})
	mux.HandleFunc("/auth/cli/exchange", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			DeviceCode string `json:"device_code"`
			Code       string `json:"code"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.DeviceCode != "dev-token" || req.Code != "code-token" {
			http.Error(w, "pairing mismatch", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(Credentials{APIToken: "et_test_key", Subdomains: []string(nil), Plan: "free"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	serverBase = srv.URL

	// "Browser": drive the grant like a user approving (in background).
	creds, err := Login(context.Background(), srv.URL, func(authURL string) error {
		// Drive the browser side synchronously: hit grant, follow the 303
		// to the loopback listener the SDK is currently serving.
		go func() {
			resp, err := http.Get(authURL)
			if err != nil {
				return // SDK will time out and the test fails on creds check
			}
			resp.Body.Close()
		}()
		return nil
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if creds.APIToken != "et_test_key" || creds.Plan != "free" {
		t.Fatalf("credentials: %+v", creds)
	}
}

// TestLoginBadRedirect locks the loopback shape rejection: a server that
// points the browser somewhere else must not make the SDK hang; the flow
// fails on timeout instead of ever receiving a code.
func TestLoginBadRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/cli/start", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"auth_url":    "http://127.0.0.1:9/auth/cli/grant?dc=x",
			"device_code": "x",
		})
	})
	mux.HandleFunc("/auth/cli/exchange", func(w http.ResponseWriter, r *http.Request) {
		t.Error("exchange must not be reached")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_, err := Login(ctx, srv.URL, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

// serverBase is the fake authorization server base URL (seam for the
// browser simulation; the SDK only ever calls it over HTTP, never ws).
var serverBase string
