package server

// #367/#368 regressions: single-list whois/kick rows carry the owner
// inline (no double write); admin create normalizes case/whitespace and
// refuses control characters / multi-@ addresses.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTapWhoisSingleMergedList(t *testing.T) {
	fake, api := newFakeTelegram(t)
	s := newTGTestServer(t, api, 42)
	seedTunnel(t, s, "who-otter-x1", "w1@x.com")

	text, _ := tapDispatch(t, s, fake, "/whois")
	// The tap row itself carries the owner: exactly one line per tunnel,
	// no separate owners block.
	if strings.Count(text, "/whois who-otter-x1") != 1 {
		t.Fatalf("whois row duplicated or missing (#367): %q", text)
	}
	if !strings.Contains(text, "/whois who-otter-x1 (w1@x.com)") {
		t.Fatalf("owner summary missing from the merged row (#367): %q", text)
	}
}

func TestTapKickSingleMergedList(t *testing.T) {
	fake, api := newFakeTelegram(t)
	s := newTGTestServer(t, api, 42)
	seedTunnel(t, s, "kick-frog-x2", "k1@x.com")

	text, _ := tapDispatch(t, s, fake, "/kick")
	if strings.Count(text, "/kick kick-frog-x2") != 1 {
		t.Fatalf("kick row duplicated or missing (#367): %q", text)
	}
	if !strings.Contains(text, "(k1@x.com)") {
		t.Fatal("owner summary missing (#367)")
	}
}

func TestValidAccountEmailRules(t *testing.T) {
	good := []string{"a@b.co", "user.tag+plus@sub.domain.io"}
	bad := []string{
		"",                       // empty
		"no-at-sign",             // no @
		"a@@b.co",                // multi-@
		"@b.co",                  // empty local
		"a@",                     // empty domain
		"a@nodot",                // domain without dot
		"a b@c.co",               // whitespace
		"a\x01@b.co",             // control char
		strings.Repeat("a", 255), // over 254
	}
	for _, e := range good {
		if !validAccountEmail(e) {
			t.Errorf("validAccountEmail(%q) = false, want true", e)
		}
	}
	for _, e := range bad {
		if validAccountEmail(e) {
			t.Errorf("validAccountEmail(%q) = true, want false (#368)", e)
		}
	}
}

// #368: admin create normalizes case and whitespace before storing.
func TestAdminCreateNormalizesEmail(t *testing.T) {
	s := newBillingTestServer(t)
	s.cfg.AdminToken = "tok"
	r := httptest.NewRequest(http.MethodPost, "/admin/accounts",
		strings.NewReader(fmt.Sprintf(`{"email":"  %s  ","tier":"free"}`, "MiXeD@X.CoM")))
	r.Header.Set("X-Admin-Token", "tok")
	w := httptest.NewRecorder()
	s.handleAdminAccounts(w, r)
	if w.Code == http.StatusBadRequest {
		t.Fatalf("normalized email rejected: %d %s (#368)", w.Code, w.Body.String())
	}
	if _, ok := s.accounts.LookupEmail("mixed@x.com"); !ok {
		t.Fatal("email not lowercased/trimmed before store (#368)")
	}
}
