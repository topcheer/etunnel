package server

import (
	"errors"
	"testing"

	"github.com/topcheer/etunnel/protocol"
)

// #112: PROTOCOL.md promises both ends ignore unknown control types. The
// server used to fail the whole tunnel on them; now it must keep serving.
// Malformed KNOWN types stay fatal (separate case below).
func TestUnknownControlTypeIgnoredTunnelSurvives(t *testing.T) {
	// handleControl-level: an unknown type returns nil (ignored) while a
	// malformed known type still errors.
	s := &Session{id: "t-unknown-ctrl"}

	if err := s.handleControl([]byte(`{"type":"some_future_extension","v":1}`)); err != nil {
		t.Fatalf("unknown control type must be ignored, got %v", err)
	}
	// Malformed known type remains fatal.
	if err := s.handleControl([]byte(`{"type":"ping","ts":"not-a-number"}`)); err == nil {
		t.Fatal("malformed known control type must stay fatal")
	}
	if err := s.handleControl([]byte(`{"type":"ping"`)); err == nil {
		t.Fatal("truncated control JSON must stay fatal")
	}
}

// Sentinel round-trip: the decoder's unknown-type error is distinguishable
// from malformed-known errors via errors.Is.
func TestErrUnknownControlTypeSentinel(t *testing.T) {
	_, err := protocol.DecodeControl([]byte(`{"type":"brand_new_thing"}`))
	if err == nil || !errors.Is(err, protocol.ErrUnknownControlType) {
		t.Fatalf("expected ErrUnknownControlType, got %v", err)
	}
	_, err = protocol.DecodeControl([]byte(`{"type":"ping","ts":"x"}`))
	if err == nil || errors.Is(err, protocol.ErrUnknownControlType) {
		t.Fatalf("malformed known type must not be classified unknown, got %v", err)
	}
}
