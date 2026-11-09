package protocol

import (
	"strings"
	"testing"
)

// Issue #14: a control message type this build does not know must be
// identifiable, so receivers can ignore it instead of dropping the session.
func TestDecodeControlMarksUnknownType(t *testing.T) {
	got, err := DecodeControl([]byte(`{"type":"future_thing","payload":1}`))
	if err == nil {
		t.Fatalf("unknown type accepted: %#v", got)
	}
	if !IsUnknownControlType(err) {
		t.Fatalf("unknown type not classified: %v", err)
	}
	if !strings.Contains(err.Error(), "future_thing") {
		t.Fatalf("error should name the type, got %v", err)
	}
}

// A malformed payload of a known type stays fatal: that is a broken peer, not
// a newer one.
func TestDecodeControlRejectsMalformedKnownType(t *testing.T) {
	_, err := DecodeControl([]byte(`{"type":"pong","ts":"not-a-number"}`))
	if err == nil || IsUnknownControlType(err) {
		t.Fatalf("malformed known type should fail as a plain error, got %v", err)
	}
}

// Issue #14: the random namespace must be wide enough to stop 400-500
// concurrent anonymous tunnels from colliding on a single draw.
func TestRandomSubdomainUsesWideSuffix(t *testing.T) {
	fourDigit := 0
	for i := 0; i < 200; i++ {
		s := RandomSubdomain()
		if err := ValidateSubdomain(s); err != nil {
			t.Fatalf("invalid name %q: %v", s, err)
		}
		parts := strings.Split(s, "-")
		if len(parts) < 3 {
			t.Fatalf("unexpected shape %q", s)
		}
		if len(parts[len(parts)-1]) == 4 {
			fourDigit++
		}
	}
	if fourDigit != 200 {
		t.Fatalf("only %d/200 names used the four-digit suffix; two-digit namespace is too small (~216k)", fourDigit)
	}
}
