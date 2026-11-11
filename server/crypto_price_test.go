package server

// Crypto (NOWPayments) minimum-price adaptation: the processor's minimum
// invoice size is dynamic per coin pair (network fees) and commonly sits at
// $3-5 on mainstream mainnets, which made the $3 Starter unpayable. The
// crypto path therefore carries its own price (cryptoPriceCents), shown on
// the pricing page's crypto button and used for the invoice.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCryptoPriceCentsOverrides(t *testing.T) {
	s := newBillingTestServer(t)

	// built-in crypto price: starter lifted above the NOWPayments floor
	cp, ok := s.cryptoPriceCents("starter")
	if !ok || cp != 500 {
		t.Fatalf("starter crypto price = %d (ok=%v), want 500", cp, ok)
	}
	// pro has no crypto-specific entry: falls back to the base price
	cp, ok = s.cryptoPriceCents("pro")
	if !ok || cp != 900 {
		t.Fatalf("pro crypto price = %d (ok=%v), want base 900", cp, ok)
	}
	// env override wins without a rebuild
	t.Setenv("TIER_STARTER_CRYPTO_PRICE_CENTS", "600")
	if cp, _ := s.cryptoPriceCents("starter"); cp != 600 {
		t.Fatalf("env override not applied: got %d, want 600", cp)
	}
	// unbuyable tier stays unbuyable
	if _, ok := s.cryptoPriceCents("free"); ok {
		t.Fatal("free tier must not be buyable via crypto")
	}
}

func TestPricingShowsCryptoPriceOnButton(t *testing.T) {
	s := newBillingTestServer(t)
	rec := httptest.NewRecorder()
	s.handlePricing(rec, httptest.NewRequest(http.MethodGet, "/pricing", nil))
	body := rec.Body.String()
	// starter's crypto button carries the lifted price; pro's stays bare
	if !strings.Contains(body, "crypto')\">Crypto &middot; $5.00/mo") {
		t.Fatalf("starter crypto button missing lifted price: %.400s", body)
	}
	if strings.Contains(body, "crypto')\">Crypto &middot; $9.00/mo") {
		t.Fatal("pro crypto button must not show a separate price")
	}
	if strings.Contains(body, "Crypto &middot; $3.00/mo") {
		t.Fatal("crypto price must not duplicate the base price")
	}
}
