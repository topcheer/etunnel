package server

import (
	"strings"
	"testing"

	"github.com/topcheer/etunnel/protocol"
)

// Issue #197: the wire-level validator must accept the same 1-63 range the
// portal sells (premium 1-2 char names included); claim-time ownership
// gating (acct.Domains) is what keeps them paid-for.
func TestValidateSubdomainBoundsAlignedWithPortal(t *testing.T) {
	cases := []struct {
		sub string
		ok  bool
	}{
		{"a", true},                      // premium 1-char, purchasable
		{"ab", true},                     // premium 2-char, purchasable
		{"abc", true},                    // previously the minimum
		{strings.Repeat("x", 41), true},  // previously rejected (>40)
		{strings.Repeat("x", 63), true},  // DNS label max
		{strings.Repeat("x", 64), false}, // one past DNS label max
		{"", false},
		{"-lead", false},
		{"trail-", false},
		{"bad_underscore", false},
		{"api", false}, // reserved
	}
	for _, c := range cases {
		err := protocol.ValidateSubdomain(c.sub)
		if (err == nil) != c.ok {
			t.Errorf("ValidateSubdomain(%q) err=%v, want ok=%v", c.sub, err, c.ok)
		}
	}
	// The portal-side checker must agree on the bounds.
	if !validSubdomain("ab") || validSubdomain(strings.Repeat("x", 64)) {
		t.Fatal("portal validSubdomain bounds disagree with 1-63")
	}
}

// Issue #189: return_to must be a strictly same-site path; backslash tricks
// and absolute URLs are rejected on both write (cookie) and read (redirect).
func TestValidReturnToRejectsBypasses(t *testing.T) {
	bad := []string{
		"",
		"//evil.com",
		"/\\evil.com", // browsers normalise \ to /
		"https://evil.com",
		"/good\r\nSet-Cookie: x=1",
		"/null\x00",
		"me/relative",
	}
	for _, v := range bad {
		if validReturnTo(v) {
			t.Errorf("validReturnTo(%q) = true, want false", v)
		}
	}
	good := []string{"/me", "/oauth/cli/grant?dc=abc", "/deep/path/here"}
	for _, v := range good {
		if !validReturnTo(v) {
			t.Errorf("validReturnTo(%q) = false, want true", v)
		}
	}
}

// Issue #195: the /me page injects the account email as a JS literal; the
// value must not contain a raw "</script>" sequence that closes the block.
func TestEmailJSValueNoScriptBreakout(t *testing.T) {
	v := string(emailJSValue("a</script>alert(1)//@x"))
	if strings.Contains(v, "</script>") || strings.Contains(v, "<") {
		t.Fatalf("email JS literal contains breakout: %q", v)
	}
	if !strings.Contains(v, "a") || !strings.HasSuffix(v, `"`) {
		t.Fatalf("email JS literal mangled: %q", v)
	}
}
