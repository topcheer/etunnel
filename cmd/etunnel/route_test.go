package main

import (
	"strings"
	"testing"

	etunnel "github.com/topcheer/etunnel/sdk"
)

func TestParseRoutes(t *testing.T) {
	got, err := parseRoutes([]string{
		"/api=127.0.0.1:8080,strip",
		"/=3000",
		"/ws=:9001",
	})
	if err != nil {
		t.Fatalf("parseRoutes: %v", err)
	}
	want := []etunnel.Route{
		{PathPrefix: "/api", Addr: "127.0.0.1:8080", StripPrefix: true},
		{PathPrefix: "/", Addr: "127.0.0.1:3000"},
		{PathPrefix: "/ws", Addr: "127.0.0.1:9001"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d routes; want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("route[%d] = %+v; want %+v", i, got[i], want[i])
		}
	}
}

func TestParseRoutesErrors(t *testing.T) {
	cases := []struct {
		name   string
		specs  []string
		substr string
	}{
		{"no equals", []string{"/api"}, "want PREFIX=ADDR"},
		{"prefix starts with =", []string{"=8080"}, "want PREFIX=ADDR"},
		{"empty addr", []string{"/api="}, "empty address"},
		{"empty addr with strip", []string{"/api=,strip"}, "empty address"},
		{"bad prefix shape", []string{"api=8080"}, "must start with"},
		{"duplicate prefix", []string{"/api=8080", "/api=9090"}, "duplicate"},
		{"dup after normalize", []string{"/api/=8080", "/api=9090"}, "duplicate"},
	}
	for _, c := range cases {
		_, err := parseRoutes(c.specs)
		if err == nil || !strings.Contains(err.Error(), c.substr) {
			t.Errorf("%s: err=%v; want substring %q", c.name, err, c.substr)
		}
	}
}

func TestParseRoutesEmpty(t *testing.T) {
	if got, err := parseRoutes(nil); err != nil || got != nil {
		t.Fatalf("nil specs = (%v,%v); want (nil,nil)", got, err)
	}
}

func TestMultiFlagContract(t *testing.T) {
	var m multiFlag
	if m.String() != "" {
		t.Fatalf("empty String() = %q", m.String())
	}
	for _, v := range []string{"a=1", "b=2"} {
		if err := m.Set(v); err != nil {
			t.Fatalf("Set(%q): %v", v, err)
		}
	}
	if m.String() != "a=1,b=2" || len(m.list) != 2 {
		t.Fatalf("Set contract broken: %q %v", m.String(), m.list)
	}
}
