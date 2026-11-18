// payments_ipn_test.go - NOWPayments IPN signing (#216) and the underpayment
// guard's type handling (#222).
//
// The pre-existing test built its signature with the same %v loop the handler
// used, and its payload had no numeric fields at all - so it was self-consistent
// and blind to the defect. Every case here therefore works from raw JSON text,
// which is what the provider actually signs.
package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// signIPN reproduces the documented payload: sorted "k=v" pairs over the raw
// JSON value text (json.Number keeps literals intact).
func signIPN(t *testing.T, secret, rawJSON string) string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(rawJSON))
	dec.UseNumber()
	var ev map[string]any
	if err := dec.Decode(&ev); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	keys := make([]string, 0, len(ev))
	for k := range ev {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		switch v := ev[k].(type) {
		case json.Number:
			sb.WriteString(v.String())
		case string:
			sb.WriteString(v)
		default:
			t.Fatalf("fixture field %s has unsupported type %T", k, v)
		}
	}
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write([]byte(sb.String()))
	return hex.EncodeToString(mac.Sum(nil))
}

// A realistic settled IPN: large integer payment_id, decimal amounts with and
// without trailing zeros. These are exactly the fields that broke before #216.
const realisticIPN = `{"payment_id":5077125051,"payment_status":"finished","price_amount":1.10,"outcome_amount":1.10,"order_id":"u@x.com:starter","pay_currency":"usdttrc20"}`

// #216: the regression that mattered - a real IPN must verify.
func TestNowPaymentsIPNAcceptsRawLiteralSignature(t *testing.T) {
	t.Cleanup(resetPaymentEventDedup)
	resetPaymentEventDedup()
	s := newBillingTestServer(t)
	s.billing.NowPaymentsIPNSecret = "ipnsecret"

	sig := signIPN(t, "ipnsecret", realisticIPN)
	r := httptest.NewRequest(http.MethodPost, "/webhooks/nowpayments", bytes.NewReader([]byte(realisticIPN)))
	r.Header.Set("x-nowpayments-sig", sig)
	rec := httptest.NewRecorder()
	s.handleNowPaymentsIPN(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("realistic IPN rejected: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := s.accounts.LookupEmail("u@x.com"); !ok {
		t.Fatal("IPN did not activate the account")
	}
}

// The old %v rendering must keep working too: a provider (or a proxy) that signs
// that way should not start failing because we changed the canonical form.
func TestNowPaymentsIPNAcceptsLegacyPercentVRendering(t *testing.T) {
	t.Cleanup(resetPaymentEventDedup)
	resetPaymentEventDedup()
	s := newBillingTestServer(t)
	s.billing.NowPaymentsIPNSecret = "ipnsecret"

	var ev map[string]any
	if err := json.Unmarshal([]byte(realisticIPN), &ev); err != nil {
		t.Fatal(err)
	}
	// Deliberately the pre-#216 construction.
	keys := make([]string, 0, len(ev))
	for k := range ev {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(k + "=")
		sb.WriteString(renderAny(ev[k]))
	}
	mac := hmac.New(sha512.New, []byte("ipnsecret"))
	mac.Write([]byte(sb.String()))
	sig := hex.EncodeToString(mac.Sum(nil))

	r := httptest.NewRequest(http.MethodPost, "/webhooks/nowpayments", bytes.NewReader([]byte(realisticIPN)))
	r.Header.Set("x-nowpayments-sig", sig)
	rec := httptest.NewRecorder()
	s.handleNowPaymentsIPN(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy-signature IPN rejected: %d", rec.Code)
	}
}

// renderAny reproduces Go's %v for a decoded `any` - which is what the pre-#216
// code used, and why 5077125051 became 5.077125051e+09.
func renderAny(v any) string { return fmt.Sprintf("%v", v) }

// A wrong secret must still fail, and a tampered body must still fail: accepting
// more canonicalisations must not have weakened anything.
func TestNowPaymentsIPNStillRejectsWrongSecretAndTampering(t *testing.T) {
	t.Cleanup(resetPaymentEventDedup)
	resetPaymentEventDedup()
	s := newBillingTestServer(t)
	s.billing.NowPaymentsIPNSecret = "ipnsecret"

	good := signIPN(t, "ipnsecret", realisticIPN)
	other := signIPN(t, "different-secret", realisticIPN)

	for name, tc := range map[string]struct{ sig, body string }{
		"wrong secret": {other, realisticIPN},
		"tampered":     {good, strings.Replace(realisticIPN, "u@x.com", "evil@x.com", 1)},
		"empty sig":    {"", realisticIPN},
	} {
		r := httptest.NewRequest(http.MethodPost, "/webhooks/nowpayments", bytes.NewReader([]byte(tc.body)))
		r.Header.Set("x-nowpayments-sig", tc.sig)
		rec := httptest.NewRecorder()
		s.handleNowPaymentsIPN(rec, r)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", name, rec.Code)
		}
	}
	if _, ok := s.accounts.LookupEmail("evil@x.com"); ok {
		t.Fatal("tampered IPN activated an account")
	}
}

// #222: a decimal sent as a JSON string must still be enforced, not treated as
// "field absent" (which fails open).
func TestNowPaymentsPaidEnoughHandlesStringAmounts(t *testing.T) {
	cases := []struct {
		name string
		ev   map[string]any
		want bool
	}{
		{"numbers, fully paid", map[string]any{"price_amount": 9.0, "outcome_amount": 9.0}, true},
		{"numbers, underpaid", map[string]any{"price_amount": 9.0, "outcome_amount": 0.1}, false},
		{"strings, fully paid", map[string]any{"price_amount": "9.00", "outcome_amount": "9.00"}, true},
		{"strings, underpaid", map[string]any{"price_amount": "9.00", "outcome_amount": "0.10"}, false},
		{"json.Number, underpaid", map[string]any{"price_amount": json.Number("9.00"), "outcome_amount": json.Number("1")}, false},
		{"both absent (documented fail-open)", map[string]any{}, true},
		{"unparseable amount refused", map[string]any{"price_amount": "not-a-number", "outcome_amount": 9.0}, false},
		{"nil price refused", map[string]any{"price_amount": nil, "outcome_amount": 9.0}, false},
	}
	for _, tc := range cases {
		if got := nowpaymentsPaidEnough(tc.ev); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
