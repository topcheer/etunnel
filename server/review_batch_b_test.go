package server

// Review batch B regression tests: #198 (webhooks answer 5xx when activation
// fails, and the provider retry is not suppressed as a replay), #190 (crypto
// success URL is a bearer capability - buyer session only), #203 (short-domain
// sweep must not tear concurrent account readers), #204 (PayPal token cache
// lifetime never goes negative), #199 (pg upsert failures are visible).

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// #198: a failed activation must answer 5xx (so the provider retries) and
// must NOT mark the event seen (so the retry reaches activate again).
func TestStripeWebhookActivationFailureReturns5xxAndRetries(t *testing.T) {
	t.Cleanup(resetPaymentEventDedup)
	resetPaymentEventDedup()
	s := newBillingTestServer(t)
	s.billing.StripeWebhookSecret = "whsec_test"
	// "no-such-tier" makes activate() fail (unknown tier).
	body := `{"id":"evt_fail_probe_1","type":"checkout.session.completed",` +
		`"data":{"object":{"payment_status":"paid","metadata":{"email":"f@x.com","tier":"no-such-tier"}}}}`

	rec := postStripeWebhook(t, s, "whsec_test", body)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("failed activation must 5xx for provider retry (#198): %d %s", rec.Code, rec.Body.String())
	}
	// The retry (same event id) must re-attempt activation, not be suppressed.
	rec2 := postStripeWebhook(t, s, "whsec_test", body)
	if rec2.Code != http.StatusInternalServerError {
		t.Fatalf("retry after failure must reach activate again: %d", rec2.Code)
	}
	if _, ok := s.accounts.LookupEmail("f@x.com"); ok {
		t.Fatal("no account must exist for a failed activation")
	}
}

// #190: the NOWPayments success URL carries a bearer payment id; only the
// buyer's session may activate with it.
func TestCryptoSuccessRejectsForeignSession(t *testing.T) {
	s := newBillingTestServer(t)
	s.auth.sessionSecret = "t"
	s.billing.NowPaymentsAPIKey = "k"
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"payment_status":"finished","order_id":"buyer@x.com:pro"}`))
	}))
	defer stub.Close()
	oldBase := nowpaymentsAPIBase
	nowpaymentsAPIBase = stub.URL
	defer func() { nowpaymentsAPIBase = oldBase }()

	get := func(cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/billing/crypto/success/777", nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		s.handleCryptoSuccess(rec, req)
		return rec
	}

	if rec := get(s.sessionCookie("attacker@x.com")); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign session: want 403, got %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := s.accounts.LookupEmail("buyer@x.com"); ok {
		t.Fatal("foreign session triggered activation")
	}
	if rec := get(nil); rec.Code != http.StatusForbidden {
		t.Fatalf("anonymous visitor: want 403, got %d", rec.Code)
	}
	if _, err := s.activate("buyer@x.com", "free"); err != nil {
		t.Fatal(err)
	}
	if rec := get(s.sessionCookie("buyer@x.com")); rec.Code != http.StatusOK {
		t.Fatalf("buyer: want 200, got %d %s", rec.Code, rec.Body.String())
	}
	if acct, ok := s.accounts.LookupEmail("buyer@x.com"); !ok || acct.Tier != "pro" {
		t.Fatal("buyer activation missing")
	}
}

// #203: reapExpiredShortDomains must build a fresh slice instead of
// compacting a.Domains in place. Lookup hands out the live *Account, so a
// reader that already holds the Domains slice (taken before the sweep, no
// further locking - the documented hot-path contract) must never observe
// its backing array rewritten underneath: with the old a.Domains[:0]
// compaction the reader's snapshot was torn in place (shifted, shortened).
func TestReapExpiredShortDomainsKeepsReaderSnapshotsIntact(t *testing.T) {
	s := newBillingTestServer(t)
	if _, err := s.activate("d@x.com", "pro"); err != nil {
		t.Fatal(err)
	}
	s.accounts.Mutate("d@x.com", func(a *Account) {
		a.Domains = []string{"ab", "xyz", "long-one.example.org", "cd"}
		a.PaidShort = &ShortDomain{Len: 2, ExpiresAt: time.Now().Add(-time.Hour)}
	})
	acct, _ := s.accounts.LookupEmail("d@x.com")
	readerSnapshot := acct.Domains // what a concurrent reader holds

	s.reapExpiredShortDomains(time.Now())

	if len(readerSnapshot) != 4 || readerSnapshot[0] != "ab" || readerSnapshot[3] != "cd" {
		t.Fatalf("reader-held snapshot was torn in place (#203): %v", readerSnapshot)
	}
	if len(acct.Domains) != 1 || acct.Domains[0] != "long-one.example.org" {
		t.Fatalf("short domains not released on the live account: %v", acct.Domains)
	}
}

// #204: the PayPal token cache lifetime must never go negative - a negative
// margin put ppTokExp in the past and re-authenticated on every call. Per
// review: expires_in <= 0 errors (response anomaly); (0,60] yields 0
// (use now, never cache); longer values keep the 60s early-refresh margin.
func TestPaypalTokenLifetimeClamp(t *testing.T) {
	for _, bad := range []int{-5, 0} {
		if _, err := paypalTokenLifetime(bad); err == nil {
			t.Fatalf("paypalTokenLifetime(%d) must error on invalid expires_in", bad)
		}
	}
	for _, short := range []int{1, 30, 60} {
		d, err := paypalTokenLifetime(short)
		if err != nil || d != 0 {
			t.Fatalf("paypalTokenLifetime(%d) = (%v,%v), want (0,nil): too short to cache", short, d, err)
		}
	}
	if d, err := paypalTokenLifetime(3600); err != nil || d != 3540*time.Second {
		t.Fatalf("paypalTokenLifetime(3600) = (%v,%v), want (3540s,nil)", d, err)
	}
}

// #199: an upsert failure in the Postgres store must be visible in the log,
// not silently swallowed ahead of the next loadAll poll.
func TestPgUpsertFailureIsVisible(t *testing.T) {
	// pgxpool.New is lazy: it does not dial until the first acquire, so a
	// pool pointed at a closed local port constructs offline and fails fast
	// on first use - no network egress needed.
	pool, err := pgxpool.New(context.Background(), "postgres://etunnel@127.0.0.1:1/etunnel")
	if err != nil {
		t.Skipf("pgxpool construction failed: %v", err)
	}
	defer pool.Close()
	st := &pgAccountStore{
		byKey:  map[string]*Account{},
		byMail: map[string]*Account{},
		pool:   pool,
	}
	a := &Account{Email: "pg@x.com", APIKey: "k1", Tier: "free"}
	st.byMail[a.Email] = a
	st.byKey[a.APIKey] = a

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	if !st.Mutate("pg@x.com", func(x *Account) { x.Tier = "pro" }) {
		t.Fatal("Mutate reported the account missing")
	}
	if !strings.Contains(buf.String(), "accounts: pg upsert") || !strings.Contains(buf.String(), "pg@x.com") {
		t.Fatalf("upsert failure not visible in log: %q", buf.String())
	}
}
