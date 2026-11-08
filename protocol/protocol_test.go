package protocol

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	payload := []byte("hello stream")
	enc := Frame{Kind: KindData, StreamID: 42, Payload: payload}.Encode()
	dec, err := ReadFrame(bytes.NewReader(enc))
	if err != nil {
		t.Fatal(err)
	}
	if dec.Kind != KindData || dec.StreamID != 42 || !bytes.Equal(dec.Payload, payload) {
		t.Fatalf("round trip mismatch: %+v", dec)
	}
}

func TestFrameZeroPayload(t *testing.T) {
	enc := Frame{Kind: KindFin, StreamID: 7}.Encode()
	dec, err := ReadFrame(bytes.NewReader(enc))
	if err != nil {
		t.Fatal(err)
	}
	if dec.Kind != KindFin || dec.StreamID != 7 || dec.Payload != nil {
		t.Fatalf("unexpected %+v", dec)
	}
}

func TestFrameRejectsBadKind(t *testing.T) {
	enc := Frame{Kind: 0x99, StreamID: 1}.Encode()
	if _, err := ReadFrame(bytes.NewReader(enc)); err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestFrameRejectsOversize(t *testing.T) {
	hdr := make([]byte, HeaderSize)
	hdr[0] = KindData
	hdr[5] = 0xFF // huge length
	if _, err := ReadFrame(bytes.NewReader(hdr)); err == nil {
		t.Fatal("expected oversize error")
	}
}

func TestControlRoundTrip(t *testing.T) {
	hello := &Hello{Type: MsgHello, Proto: ProtoVersion, Subdomain: "abc"}
	b, err := EncodeControl(hello)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeControl(b)
	if err != nil {
		t.Fatal(err)
	}
	if h, ok := got.(*Hello); !ok || h.Subdomain != "abc" || h.Proto != ProtoVersion {
		t.Fatalf("hello round trip mismatch: %#v", got)
	}

	res := &Res{Type: MsgRes, StreamID: 3, Status: 200, Headers: map[string][]string{"X-A": {"1", "2"}}}
	b, err = EncodeControl(res)
	if err != nil {
		t.Fatal(err)
	}
	got, err = DecodeControl(b)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := got.(*Res); !ok || r.StreamID != 3 || r.Status != 200 || len(r.Headers["X-A"]) != 2 {
		t.Fatalf("res round trip mismatch: %#v", got)
	}
}

func TestDecodeControlRejectsGarbage(t *testing.T) {
	if _, err := DecodeControl([]byte("not json")); err == nil {
		t.Fatal("expected error")
	}
	if _, err := DecodeControl([]byte(`{"type":"nope"}`)); err == nil {
		t.Fatal("expected unknown type error")
	}
}

func TestValidateSubdomain(t *testing.T) {
	good := []string{"abc", "my-dev-box", "x9z"}
	bad := []string{"ab", "-abc", "abc-", "a_b", "a.b", "", strings.Repeat("a", 41), "WWW", "www", "admin"}
	for _, s := range good {
		if err := ValidateSubdomain(s); err != nil {
			t.Errorf("%q should be valid: %v", s, err)
		}
	}
	for _, s := range bad {
		if err := ValidateSubdomain(s); err == nil {
			t.Errorf("%q should be invalid", s)
		} else if !errors.Is(err, ErrBadSubdomain) {
			t.Errorf("%q: expected ErrBadSubdomain, got %v", s, err)
		}
	}
}

func TestRandomSubdomain(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		s := RandomSubdomain()
		if err := ValidateSubdomain(s); err != nil {
			t.Fatalf("generated invalid subdomain %q: %v", s, err)
		}
		seen[s] = true
	}
	if len(seen) < 50 {
		t.Fatalf("poor entropy: %d unique in 100", len(seen))
	}
}
