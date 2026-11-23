package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/topcheer/etunnel/protocol"
)

// --- #381: tap buttons must re-send commands that resolve ----------------

// The tap loop: build the parameter-less list reply, take every BUTTON row,
// re-dispatch it verbatim (exactly what a Telegram tap does) and assert the
// command resolves against the live registry - no "no such tunnel", and a
// tapped /kick really closes the session.
func TestTapRowsAreExecutableCommands(t *testing.T) {
	s := newBillingTestServer(t)
	_, _ = seededSession(t, s, "tap@x.com", "tap-auth-1")
	anon := &Session{
		srv: s, id: "sess-tap-anon", sub: "tap-anon-1",
		url: "https://tap-anon-1.t.test", ip: "203.0.113.9:4444",
		closed: make(chan struct{}),
	}
	if err := s.reg.Acquire("tap-anon-1", anon); err != nil {
		t.Fatal(err)
	}

	// /whois: owner annotation lives on the (non-clickable) body lines.
	body, kb := s.handleAdminCommandKB("/whois")
	if !strings.Contains(body, "(tap@x.com)") || !strings.Contains(body, "(anonymous)") {
		t.Fatalf("whois body lost the owner annotation: %q", body)
	}
	assertTapResolves(t, s, "/whois", kb)

	// /kick: tapping each row kicks the tunnel it names.
	_, kb = s.handleAdminCommandKB("/kick")
	assertTapResolves(t, s, "/kick", kb)

	select {
	case <-anon.closed:
	default:
		t.Fatal("anonymous tunnel survived a tapped /kick (#381)")
	}
	if s.reg.Lookup("tap-auth-1") != nil {
		t.Fatal("authed tunnel survived a tapped /kick (#381)")
	}
}

// assertTapResolves re-dispatches every button row (the menu row excluded)
// through the real KB dispatcher and requires the command to resolve.
func assertTapResolves(t *testing.T, s *Server, cmd string, kb [][]string) {
	t.Helper()
	if len(kb) < 2 {
		t.Fatalf("%s: keyboard too small: %v", cmd, kb)
	}
	for _, row := range kb[:len(kb)-1] { // last row is the menu pair
		text := row[0]
		if strings.ContainsAny(text, "()") || strings.Contains(text, "anonymous") {
			t.Fatalf("%s button row must be a bare command (#381): %q", cmd, text)
		}
		out, _ := s.handleAdminCommandKB(text)
		if strings.Contains(out, "no such tunnel") {
			t.Fatalf("%s tapped row %q does not resolve (#381): %s", cmd, text, out)
		}
	}
}

// --- #389: tapRows header budget + keyboard row cap -----------------------

func TestTapRowsHeaderBudgetAndRowCap(t *testing.T) {
	rows := make([]string, maxTappableRows+6)
	for i := range rows {
		rows[i] = fmt.Sprintf("/kick s%d", i)
	}
	kb, body := tapRowsWith("tap a tunnel to kick:", rows, nil)
	if got := len(kb) - 1; got != maxTappableRows { // -1: menu row
		t.Fatalf("keyboard rows = %d, want the %d cap (#389 dead constant)", got, maxTappableRows)
	}
	if !strings.Contains(body, "and 6 more") {
		t.Fatalf("omitted count not reported: %q", body)
	}

	// The caller's header line counts against the byte budget (#389) - a
	// near-limit header leaves room for no rows and the body stays in budget.
	big := strings.Repeat("x", telegramMsgLimit-100)
	kb2, body2 := tapRowsWith(big, rows[:2], nil)
	if got := len(kb2) - 1; got != 0 {
		t.Fatalf("header budget ignored: kept %d rows", got)
	}
	if len(body2) > telegramMsgLimit {
		t.Fatalf("body exceeds the telegram limit: %d", len(body2))
	}

	// Buttons stay pure commands; notes enrich only the body (#381).
	kb3, body3 := tapRowsWith("hdr:", []string{"/kick a", "/kick b"},
		[]string{"/kick a (o@x.com)", "/kick b (anonymous)"})
	if kb3[0][0] != "/kick a" || kb3[1][0] != "/kick b" {
		t.Fatalf("buttons polluted by notes: %v", kb3)
	}
	if !strings.Contains(body3, "/kick a (o@x.com)") || !strings.Contains(body3, "/kick b (anonymous)") {
		t.Fatalf("notes missing from body: %q", body3)
	}
}

// --- #386: one shared account-email validator ------------------------------

func TestRegisterEmailValidationMatrix(t *testing.T) {
	s := newBillingTestServer(t)
	bad := []string{
		"",
		"noat.com",                          // no @
		"@x.com",                            // empty local
		"a@",                                // empty domain
		"a@b@c.com",                         // multi-@ (register used to mint these)
		"a@x",                               // dotless domain (register used to mint these)
		"a\x07b@x.com",                      // control char (register used to mint these)
		strings.Repeat("a", 250) + "@x.com", // 256 > 254
	}
	for _, e := range bad {
		s.regLimiter = &registerLimiter{seen: make(map[string]time.Time)} // fresh bucket per attempt
		req := httptest.NewRequest(http.MethodPost, "/api/register",
			strings.NewReader(fmt.Sprintf(`{"email":%q,"tier":"free"}`, e)))
		rec := httptest.NewRecorder()
		s.handleRegister(rec, req)
		if rec.Code < 400 {
			t.Fatalf("register accepted %q: %d (#386)", e, rec.Code)
		}
		if _, ok := s.accounts.LookupEmail(e); ok {
			t.Fatalf("register minted an account for %q (#386)", e)
		}
	}
	// valid email still registers
	s.regLimiter = &registerLimiter{seen: make(map[string]time.Time)}
	req := httptest.NewRequest(http.MethodPost, "/api/register",
		strings.NewReader(`{"email":"bob386@x.com","tier":"free"}`))
	rec := httptest.NewRecorder()
	s.handleRegister(rec, req)
	if rec.Code >= 400 {
		t.Fatalf("valid registration rejected: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := s.accounts.LookupEmail("bob386@x.com"); !ok {
		t.Fatal("valid registration did not create the account")
	}
	// the shared gate rejects what the admin API rejects (#386: one validator)
	if _, err := s.createAccountChecked("dirty@x", "free"); err == nil {
		t.Fatal("createAccountChecked minted a dotless-domain account (#386)")
	}
	if _, err := s.createAccountChecked("ok386@x.com", "free"); err != nil {
		t.Fatalf("createAccountChecked rejected a valid email: %v", err)
	}
}

// --- #389: transient attribution failure must 5xx (provider retries) ------

func TestStripeDisputeLookupFailureRetries(t *testing.T) {
	s := newBillingTestServer(t)
	if _, err := s.activate("sl389@x.com", "pro"); err != nil {
		t.Fatal(err)
	}
	var fail atomic.Bool
	fail.Store(true)
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/v1/charges/C_sl389") {
			if fail.Load() {
				http.Error(w, "boom", http.StatusBadGateway)
				return
			}
			fmt.Fprint(w, `{"id":"C_sl389","metadata":{"email":"sl389@x.com"}}`)
			return
		}
		fmt.Fprint(w, "{}")
	}))
	defer fs.Close()
	old := stripeHTTP
	stripeHTTP = &http.Client{Transport: rewriteTransport{base: fs.URL}, Timeout: old.Timeout}
	t.Cleanup(func() { stripeHTTP = old })

	const secret = "whsec_389sl"
	s.billing.StripeWebhookSecret = secret
	body := `{"id":"evt-389sl","type":"charge.dispute.created","data":{"object":{"id":"dp_sl","charge":"C_sl389","amount":900,"metadata":{}}}}`

	// First delivery: charge lookup fails transiently -> 5xx, no mark, no
	// rollback (answering 200 would lose the dispute forever).
	if rec := postStripeWebhook(t, s, secret, body); rec.Code != http.StatusInternalServerError {
		t.Fatalf("transient lookup failure must 5xx (#389): %d", rec.Code)
	}
	if a, _ := s.accounts.LookupEmail("sl389@x.com"); a.Tier != "pro" {
		t.Fatal("lookup failure rolled back the entitlement (#389)")
	}
	// Provider retries: lookup recovered -> rollback applies and sticks.
	fail.Store(false)
	if rec := postStripeWebhook(t, s, secret, body); rec.Code != http.StatusOK {
		t.Fatalf("retry after recovery: %d %s", rec.Code, rec.Body.String())
	}
	if a, _ := s.accounts.LookupEmail("sl389@x.com"); a.Tier != "free" {
		t.Fatal("recovered lookup did not roll back (#389)")
	}
}

func TestPayPalAttributionLookupFailureRetries(t *testing.T) {
	s := newBillingTestServer(t)
	if _, err := s.activate("pl389@x.com", "pro"); err != nil {
		t.Fatal(err)
	}
	var fail atomic.Bool
	fail.Store(true)
	pp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/token"):
			fmt.Fprint(w, `{"access_token":"tok","expires_in":3600}`)
		case strings.Contains(r.URL.Path, "CAP-389"):
			if fail.Load() {
				http.Error(w, "boom", http.StatusBadGateway)
				return
			}
			// #380/#391: real capture-lookup shape - TOP-LEVEL custom_id.
			fmt.Fprint(w, `{"id":"CAP-389","custom_id":"pl389@x.com","status":"COMPLETED"}`)
		default:
			fmt.Fprint(w, `{"verification_status":"SUCCESS"}`)
		}
	}))
	defer pp.Close()
	old := paypalHTTP
	paypalHTTP = &http.Client{Transport: rewriteTransport{base: pp.URL}, Timeout: old.Timeout}
	resetPayPalTokenCache()
	t.Cleanup(func() { paypalHTTP = old; resetPayPalTokenCache() })
	s.billing.PayPalWebhookID = "wh1"
	s.billing.PayPalClientID = "cid"
	s.billing.PayPalSecret = "sec"

	body := `{"id":"evt-389pl","event_type":"CUSTOMER.DISPUTE.CREATED","resource":{"disputed_transactions":[{"seller_transaction_id":"CAP-389"}]}}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks/paypal", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handlePayPalWebhook(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("transient capture lookup must 5xx (#389): %d", rec.Code)
	}
	if a, _ := s.accounts.LookupEmail("pl389@x.com"); a.Tier != "pro" {
		t.Fatal("lookup failure rolled back the entitlement (#389)")
	}
	fail.Store(false)
	req = httptest.NewRequest(http.MethodPost, "/webhooks/paypal", strings.NewReader(body))
	rec = httptest.NewRecorder()
	s.handlePayPalWebhook(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry after recovery: %d %s", rec.Code, rec.Body.String())
	}
	if a, _ := s.accounts.LookupEmail("pl389@x.com"); a.Tier != "free" {
		t.Fatal("recovered lookup did not roll back (#389)")
	}
}

// --- #389: loadAll tombstones (deleted rows must not resurrect) -----------

func TestPGTombstonesSuppressResurrection(t *testing.T) {
	s := &pgAccountStore{}
	k1 := &Account{APIKey: "k1", Email: "k1@x.com"}
	k2 := &Account{APIKey: "k2", Email: "k2@x.com"}
	s.tombstone("k1") // a DeleteWithError that committed while loadAll ran
	byKey := map[string]*Account{"k1": k1, "k2": k2}
	byMail := map[string]*Account{"k1@x.com": k1, "k2@x.com": k2}
	s.sweepTombstones(byKey, byMail)
	if _, ok := byKey["k1"]; ok {
		t.Fatal("deleted row resurrected from a pre-delete loadAll snapshot (#389)")
	}
	if _, ok := byMail["k1@x.com"]; ok {
		t.Fatal("deleted email resurrected (#389)")
	}
	if _, ok := byKey["k2"]; !ok {
		t.Fatal("live row swept by mistake (#389)")
	}
	// A stale tombstone expires and stops suppressing (a re-created account
	// must not vanish from loads forever).
	s.delMu.Lock()
	s.recentlyDeleted["k1"] = time.Now().Add(-2 * recentDeleteTTL)
	s.delMu.Unlock()
	byKey["k1"] = k1
	byMail["k1@x.com"] = k1
	s.sweepTombstones(byKey, byMail)
	if _, ok := byKey["k1"]; !ok {
		t.Fatal("stale tombstone still suppressing rows (#389)")
	}
}

// --- #389: the #366 recheck must not Release an unclaimed router name ------

// spyRouter records cluster ops per subdomain in order.
type spyRouter struct {
	mu  sync.Mutex
	ops map[string][]string
}

func newSpyRouter() *spyRouter { return &spyRouter{ops: map[string][]string{}} }

func (r *spyRouter) record(sub, op string) {
	r.mu.Lock()
	r.ops[sub] = append(r.ops[sub], op)
	r.mu.Unlock()
}
func (r *spyRouter) Lookup(string) string                                    { return "" }
func (r *spyRouter) Claim(sub string) error                                  { r.record(sub, "claim"); return nil }
func (r *spyRouter) Release(sub string)                                      { r.record(sub, "release") }
func (r *spyRouter) Forward(string, http.ResponseWriter, *http.Request) bool { return false }
func (r *spyRouter) Close() error                                            { return nil }

// delayedSnapshotStore makes every Snapshot call for a given key EXCEPT the
// first slow: a handshake's auth-time lookup is fast, its #366 post-acquire
// recheck is slow - parking the recheck past a concurrent delete makes the
// revoke-mid-handshake branch deterministic instead of a microsecond race.
type delayedSnapshotStore struct {
	accountBackend
	delay time.Duration
	mu    sync.Mutex
	seen  map[string]int
}

func (s *delayedSnapshotStore) Snapshot(key string) (Account, bool) {
	s.mu.Lock()
	if s.seen == nil {
		s.seen = map[string]int{}
	}
	s.seen[key]++
	n := s.seen[key]
	s.mu.Unlock()
	if n > 1 {
		time.Sleep(s.delay)
	}
	return s.accountBackend.Snapshot(key)
}

func TestRecheckRevocationNeverReleasesUnclaimedRouterName(t *testing.T) {
	s, _, ts := quotaTestServer(t, nil)
	spy := newSpyRouter()
	s.router = spy
	s.accounts = &delayedSnapshotStore{accountBackend: s.accounts, delay: 150 * time.Millisecond}

	var logMu sync.Mutex
	var logged strings.Builder
	oldLog := logPrintf
	logPrintf = func(f string, a ...any) (int, error) {
		logMu.Lock()
		defer logMu.Unlock()
		return fmt.Fprintf(&logged, f, a...)
	}
	t.Cleanup(func() { logPrintf = oldLog })

	acct, err := s.accounts.Create("rc389@x.com", "free")
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/tunnel", nil)
			if err != nil {
				return
			}
			hello, _ := json.Marshal(&protocol.Hello{Type: protocol.MsgHello, Proto: protocol.ProtoVersion, Token: acct.APIKey})
			if ws.Write(ctx, websocket.MessageText, hello) != nil {
				ws.Close(websocket.StatusNormalClosure, "")
				return
			}
			for {
				if _, _, err := ws.Read(ctx); err != nil {
					break
				}
			}
			ws.Close(websocket.StatusNormalClosure, "")
		}()
	}
	time.Sleep(40 * time.Millisecond)
	// Store-level delete only: the recheck branch under test must unwind the
	// REGISTRY acquire itself. (kickSessionsByAccount would Close sessions
	// that never reached Claim, and Session.Close broadcasts a router
	// Release for its sub unconditionally - pre-existing Close semantics,
	// out of scope for the #389 recheck regression this test pins.)
	if err := s.accounts.DeleteWithError(acct.APIKey); err != nil {
		t.Fatalf("delete: %v", err)
	}
	wg.Wait()

	// The revoke-mid-handshake branch must have executed, or this test is
	// vacuous (the wrapped store parked the recheck past the delete).
	logMu.Lock()
	loggedText := logged.String()
	logMu.Unlock()
	if !strings.Contains(loggedText, "token revoked mid-handshake") {
		t.Fatal("revoke-mid-handshake branch never ran - test window missed")
	}
	// Invariant: a router Release is only ever seen after that sub's Claim
	// - the recheck must unwind the REGISTRY acquire only (#389).
	spy.mu.Lock()
	defer spy.mu.Unlock()
	for sub, ops := range spy.ops {
		claimed := false
		for _, op := range ops {
			switch op {
			case "claim":
				claimed = true
			case "release":
				if !claimed {
					t.Fatalf("router.Release(%q) without a prior Claim (#389 recheck regression); ops=%v", sub, ops)
				}
			}
		}
	}
}
