package server

// #94: webhook payloads that fail to parse must be rejected (400 + log),
// not silently acknowledged with 200 - providers treat 200 as delivered
// and never retry, so activation events vanished.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func stripeSign(secret, body string) string {
	tt := fmt.Sprintf("%d", time.Now().Unix())
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(tt + "." + body))
	return "t=" + tt + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestWebhookRejectsUnparseablePayload(t *testing.T) {
	s := newBillingTestServer(t)
	s.billing.StripeWebhookSecret = "whsec_x"

	// syntactically valid signature over a truncated (unparseable) body:
	// verification passes, the Unmarshal branch must 400 instead of 200.
	body := `{"type":"checkout.session.com`
	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(body))
	req.Header.Set("Stripe-Signature", stripeSign("whsec_x", body))
	rec := httptest.NewRecorder()
	s.handleStripeWebhook(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("stripe bad payload: want 400, got %d %s", rec.Code, rec.Body.String())
	}
}
