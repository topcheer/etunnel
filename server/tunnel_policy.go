// Tunnel policy: per-tunnel edge protection declared in the hello message
// (feature-gap analysis #1 and #3). Three independent, composable layers
// evaluated on every visitor request BEFORE any stream is opened:
//
//	basicAuth  - HTTP Basic (401 + WWW-Authenticate on mismatch)
//	allowIPs   - source CIDR allowlist (403 outside)
//	webhooks   - signature verification for known webhook providers
//	            (Stripe / GitHub / GitLab). Requests carrying a known
//	            provider's signature header are verified against the raw
//	            body and rejected 401 on mismatch; requests WITHOUT any
//	            known signature header fall through to the other layers
//	            (verification is hardening, not a blanket close).
//
// Backward compatibility: an absent policy behaves exactly as before - old
// clients that never send hello.policy are unaffected.
package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// WebhookRule pins one provider's verification secret.
type WebhookRule struct {
	Provider string // stripe | github | gitlab
	Secret   string
}

// TunnelPolicy is the parsed hello.policy object.
type TunnelPolicy struct {
	BasicUser string
	BasicPass string
	AllowIPs  []*net.IPNet // parsed CIDRs (single IPs become /32 or /128)
	Webhooks  []WebhookRule
}

// rawPolicy mirrors the wire JSON (protocol.Hello.Policy).
type rawPolicy struct {
	BasicAuth string    `json:"basicAuth,omitempty"` // "user:pass"
	AllowIPs  []string  `json:"allowIPs,omitempty"`  // CIDRs or plain IPs
	Webhooks  []rawHook `json:"webhooks,omitempty"`
}

type rawHook struct {
	Provider string `json:"provider"`
	Secret   string `json:"secret"`
}

// parseTunnelPolicy validates the raw hello fields into an executable
// policy. nil policy = no protection (legacy behavior).
func parseTunnelPolicy(raw *rawPolicy) (*TunnelPolicy, error) {
	if raw == nil {
		return nil, nil
	}
	tp := &TunnelPolicy{}
	if raw.BasicAuth != "" {
		u, p, ok := strings.Cut(raw.BasicAuth, ":")
		if !ok || u == "" {
			return nil, fmt.Errorf("etunnel: policy.basicAuth must be \"user:pass\"")
		}
		tp.BasicUser, tp.BasicPass = u, p
	}
	for _, c := range raw.AllowIPs {
		if !strings.Contains(c, "/") {
			if ip := net.ParseIP(c); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				c = fmt.Sprintf("%s/%d", c, bits)
			}
		}
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("etunnel: policy.allowIPs: bad CIDR %q", c)
		}
		tp.AllowIPs = append(tp.AllowIPs, n)
	}
	for _, h := range raw.Webhooks {
		switch h.Provider {
		case "stripe", "github", "gitlab":
			if h.Secret == "" {
				return nil, fmt.Errorf("etunnel: policy.webhooks: %s needs a secret", h.Provider)
			}
			tp.Webhooks = append(tp.Webhooks, WebhookRule{Provider: h.Provider, Secret: h.Secret})
		default:
			return nil, fmt.Errorf("etunnel: policy.webhooks: unknown provider %q (stripe|github|gitlab)", h.Provider)
		}
	}
	if tp.BasicUser == "" && len(tp.AllowIPs) == 0 && len(tp.Webhooks) == 0 {
		return nil, fmt.Errorf("etunnel: policy is empty")
	}
	return tp, nil
}

// clientIP resolves the visitor source IP. Behind a reverse proxy the
// socket address is the proxy's; the standard forwarded headers are
// honoured in order of specificity (CF-Connecting-IP, X-Real-IP, first
// X-Forwarded-For hop). Deployments NOT behind a trusted proxy should
// strip those headers at the edge (documented in PROTOCOL.md).
func clientIP(r *http.Request) net.IP {
	if v := r.Header.Get("CF-Connecting-IP"); v != "" {
		if ip := net.ParseIP(strings.TrimSpace(v)); ip != nil {
			return ip
		}
	}
	if v := r.Header.Get("X-Real-IP"); v != "" {
		if ip := net.ParseIP(strings.TrimSpace(v)); ip != nil {
			return ip
		}
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if ip := net.ParseIP(strings.TrimSpace(strings.SplitN(v, ",", 2)[0])); ip != nil {
			return ip
		}
	}
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return net.ParseIP(r.RemoteAddr)
	}
	return net.ParseIP(h)
}

// allowIP reports whether the request source is inside the allowlist.
func (tp *TunnelPolicy) allowIP(ip net.IP) bool {
	if len(tp.AllowIPs) == 0 {
		return true
	}
	for _, n := range tp.AllowIPs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// checkBasic returns true when the request satisfies the Basic layer
// (or the layer is off). On failure it has already written the 401.
func (tp *TunnelPolicy) checkBasic(w http.ResponseWriter, r *http.Request) bool {
	if tp.BasicUser == "" {
		return true
	}
	u, p, ok := r.BasicAuth()
	if !ok || !constantTimeEq(u, tp.BasicUser) || !constantTimeEq(p, tp.BasicPass) {
		w.Header().Set("WWW-Authenticate", `Basic realm="etunnel", charset="UTF-8"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func constantTimeEq(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}

// webhookHeaderFor maps a provider to (header, present).
func webhookHeaderFor(r *http.Request, provider string) (string, bool) {
	switch provider {
	case "stripe":
		v := r.Header.Get("Stripe-Signature")
		return v, v != ""
	case "github":
		v := r.Header.Get("X-Hub-Signature-256")
		return v, v != ""
	case "gitlab":
		v := r.Header.Get("X-Gitlab-Token")
		return v, v != ""
	}
	return "", false
}

// verifyWebhook checks the provider signature over the raw body.
// Returns the provider name on success, "" when no known signature
// header is present (caller falls through), and an error on mismatch.
func (tp *TunnelPolicy) verifyWebhook(r *http.Request, body []byte) (string, error) {
	for _, h := range tp.Webhooks {
		val, present := webhookHeaderFor(r, h.Provider)
		if !present {
			continue
		}
		switch h.Provider {
		case "stripe":
			if err := verifyStripe(val, body, h.Secret); err != nil {
				return "", err
			}
		case "github":
			mac := hmac.New(sha256.New, []byte(h.Secret))
			mac.Write(body)
			want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
			if !hmac.Equal([]byte(want), []byte(val)) {
				return "", fmt.Errorf("github signature mismatch")
			}
		case "gitlab":
			if !constantTimeEq(val, h.Secret) {
				return "", fmt.Errorf("gitlab token mismatch")
			}
		}
		return h.Provider, nil
	}
	return "", nil
}

// verifyStripe validates "t=...,v1=..." (v1 = HMAC-SHA256 hex over
// "timestamp.body"), tolerating a 5-minute clock skew.
func verifyStripe(sigHeader string, body []byte, secret string) error {
	var ts, v1 string
	for _, part := range strings.Split(sigHeader, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts = v
		case "v1":
			v1 = v
		}
	}
	if ts == "" || v1 == "" {
		return fmt.Errorf("stripe signature header malformed")
	}
	t64, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fmt.Errorf("stripe timestamp malformed")
	}
	if d := time.Since(time.Unix(t64, 0)); d > 5*time.Minute || d < -5*time.Minute {
		return fmt.Errorf("stripe timestamp outside tolerance")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(v1)) {
		return fmt.Errorf("stripe signature mismatch")
	}
	return nil
}

// apply evaluates the request against every layer. ok=false means the
// response has been written (401/403) and the tunnel must not serve it.
// On webhook success the verification marker header is added for the
// local service to trust.
func (tp *TunnelPolicy) apply(w http.ResponseWriter, r *http.Request, body []byte) bool {
	if tp == nil {
		return true
	}
	if ip := clientIP(r); !tp.allowIP(ip) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	if !tp.checkBasic(w, r) {
		return false
	}
	if len(tp.Webhooks) > 0 {
		provider, err := tp.verifyWebhook(r, body)
		if err != nil {
			http.Error(w, "webhook verification failed: "+err.Error(), http.StatusUnauthorized)
			return false
		}
		if provider != "" {
			r.Header.Set("X-Etunnel-Webhook", "verified: "+provider)
		}
	}
	return true
}

// needsBody reports whether this policy requires reading the request
// body before forwarding (webhook verification does; the other layers
// do not, and streaming stays streaming).
func (tp *TunnelPolicy) needsBody() bool {
	return tp != nil && len(tp.Webhooks) > 0
}

// readBodyForVerify buffers the request body (bounded) for webhook
// verification and returns a replay reader so forwarding continues
// unchanged.
func readBodyForVerify(r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	buf := new(bytes.Buffer)
	n, err := copyBounded(buf, r.Body, limit)
	if err != nil {
		return nil, err
	}
	if n >= limit {
		return nil, fmt.Errorf("etunnel: webhook body exceeds %d bytes", limit)
	}
	r.Body = newReplayBody(buf.Bytes())
	return buf.Bytes(), nil
}

// copyBounded copies at most limit bytes, reporting the count copied.
func copyBounded(dst *bytes.Buffer, src io.Reader, limit int64) (int64, error) {
	n, err := io.Copy(dst, io.LimitReader(src, limit+1))
	return n, err
}

// replayBody is an io.ReadCloser over an in-memory buffer.
type replayBody struct {
	r *bytes.Reader
}

func newReplayBody(b []byte) *replayBody { return &replayBody{bytes.NewReader(b)} }
func (x *replayBody) Read(p []byte) (int, error) { return x.r.Read(p) }
func (x *replayBody) Close() error { return nil }
