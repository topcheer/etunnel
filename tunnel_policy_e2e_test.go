package etunnel_test

// Tunnel policy E2E (feature-gap #1/#3): a protected tunnel rejects at the
// edge, accepts with credentials, verifies webhook signatures, and a
// policy-less tunnel behaves exactly as before.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"

	etunnel "github.com/topcheer/etunnel/sdk"
)

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
func addrOf(localURL string) string      { return strings.TrimPrefix(localURL, "http://") }

func policyTun(t *testing.T, srvURL, local string, pol *etunnel.TunnelPolicy) *etunnel.Tunnel {
	t.Helper()
	tun, err := etunnel.Connect(context.Background(), etunnel.Options{
		Server:    sdkURL(srvURL),
		LocalAddr: local,
		Policy:    pol,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { tun.Close("test done") })
	return tun
}

func status(t *testing.T, client *http.Client, url string, mold func(*http.Request)) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if mold != nil {
		mold(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestTunnelPolicyE2E(t *testing.T) {
	_, srvURL := startTestServer(t, nil)
	client := clientThroughServer(t, srvURL)

	var sawWebhook string
	local := newLocalService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawWebhook = r.Header.Get("X-Etunnel-Webhook")
		w.WriteHeader(http.StatusOK)
	}))
	localAddr := addrOf(local)
	// 1. basicAuth: 401 without creds, 200 with.
	tun1 := policyTun(t, srvURL, localAddr, &etunnel.TunnelPolicy{BasicAuth: "u:p"})
	if c := status(t, client, tun1.URL(), nil); c != http.StatusUnauthorized {
		t.Fatalf("no creds = %d, want 401", c)
	}
	if c := status(t, client, tun1.URL(), func(r *http.Request) { r.SetBasicAuth("u", "p") }); c != http.StatusOK {
		t.Fatalf("with creds = %d, want 200", c)
	}

	// 2. webhook (github): bad sig 401 at the edge; good sig 200 and the
	// local service saw the marker header.
	const ghSecret = "gh_e2e_secret"
	tun2 := policyTun(t, srvURL, localAddr, &etunnel.TunnelPolicy{
		Webhooks: []etunnel.WebhookRule{{Provider: "github", Secret: ghSecret}},
	})
	body := []byte(`{"zen":"keep it simple"}`)
	mac := hmac.New(sha256.New, []byte(ghSecret))
	mac.Write(body)
	goodSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	post := func(url, sig string, b []byte) int {
		return status(t, client, url, func(r *http.Request) {
			r.Method = http.MethodPost
			r.Header.Set("X-Hub-Signature-256", sig)
			r.Body = io.NopCloser(bytesReader(b))
			r.ContentLength = int64(len(b))
		})
	}
	if c := post(tun2.URL(), "sha256=badbadbad", body); c != http.StatusUnauthorized {
		t.Fatalf("bad webhook sig = %d, want 401", c)
	}
	if c := post(tun2.URL(), goodSig, body); c != http.StatusOK {
		t.Fatalf("good webhook sig = %d, want 200", c)
	}
	if sawWebhook != "verified: github" {
		t.Fatalf("local saw X-Etunnel-Webhook = %q", sawWebhook)
	}

	// 3. allowIPs: visitor 127.0.0.1 outside the list -> 403; the CF header
	// route is honoured (documented proxy-trust model).
	tun3 := policyTun(t, srvURL, localAddr, &etunnel.TunnelPolicy{AllowIPs: []string{"10.99.0.0/16"}})
	if c := status(t, client, tun3.URL(), nil); c != http.StatusForbidden {
		t.Fatalf("outside allowIPs = %d, want 403", c)
	}
	if c := status(t, client, tun3.URL(), func(r *http.Request) { r.Header.Set("CF-Connecting-IP", "10.99.1.2") }); c != http.StatusOK {
		t.Fatalf("inside allowIPs via CF header = %d, want 200", c)
	}

	// 4. legacy regression pin: no policy behaves exactly as before.
	tun4 := policyTun(t, srvURL, localAddr, nil)
	if c := status(t, client, tun4.URL(), nil); c != http.StatusOK {
		t.Fatalf("legacy no-policy = %d, want 200", c)
	}
}
