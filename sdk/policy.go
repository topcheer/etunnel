package etunnel

import "encoding/json"

// TunnelPolicy is the SDK-side declaration of per-tunnel edge protection.
// It serializes into hello.policy and is enforced by the server: visitor
// requests are checked at the edge BEFORE any stream reaches the local
// service. Every layer is optional; zero fields = no policy is sent.
//
//	// demo behind a password
//	etunnel.Connect(etunnel.Options{..., Policy: &etunnel.TunnelPolicy{BasicAuth: "u:p"}})
//	// Stripe webhooks verified at the edge
//	Policy: &etunnel.TunnelPolicy{Webhooks: []etunnel.WebhookRule{{Provider: "stripe", Secret: "whsec_..."}}}
type TunnelPolicy struct {
	// BasicAuth protects the tunnel with HTTP Basic ("user:pass").
	BasicAuth string `json:"basicAuth,omitempty"`
	// AllowIPs restricts visitors to these CIDRs / plain IPs.
	AllowIPs []string `json:"allowIPs,omitempty"`
	// Webhooks verifies signatures of the listed providers at the edge;
	// verified requests carry X-Etunnel-Webhook to the local service.
	Webhooks []WebhookRule `json:"webhooks,omitempty"`
}

// WebhookRule pins one provider's verification secret. Providers:
// stripe (Stripe-Signature, 5-min tolerance), github
// (X-Hub-Signature-256), gitlab (X-Gitlab-Token).
type WebhookRule struct {
	Provider string `json:"provider"`
	Secret   string `json:"secret"`
}

// policyJSON serializes the policy for the hello message; nil (or an
// all-empty policy) produces nil so the wire stays identical to legacy.
func policyJSON(p *TunnelPolicy) json.RawMessage {
	if p == nil || (p.BasicAuth == "" && len(p.AllowIPs) == 0 && len(p.Webhooks) == 0) {
		return nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil // unreachable: struct of strings/slices
	}
	return b
}
