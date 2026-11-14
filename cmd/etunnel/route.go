package main

import (
	"fmt"
	"strings"

	etunnel "github.com/topcheer/etunnel/sdk"
)

// multiFlag collects a repeatable string flag (-route a -route b).
type multiFlag struct{ list []string }

func (m *multiFlag) String() string { return strings.Join(m.list, ",") }
func (m *multiFlag) Set(v string) error {
	m.list = append(m.list, v)
	return nil
}

// parseRoutes turns "-route PREFIX=ADDR[,strip]" strings into SDK Routes.
// ADDR may be a bare port ("8080") which becomes 127.0.0.1:8080, matching
// the CLI's single-target convenience for positional ports.
func parseRoutes(specs []string) ([]etunnel.Route, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	out := make([]etunnel.Route, 0, len(specs))
	for i, s := range specs {
		eq := strings.IndexByte(s, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("route[%d]: want PREFIX=ADDR[,strip], got %q", i, s)
		}
		prefix, rest := s[:eq], s[eq+1:]
		strip := false
		if strings.HasSuffix(rest, ",strip") {
			strip = true
			rest = strings.TrimSuffix(rest, ",strip")
		}
		if rest == "" {
			return nil, fmt.Errorf("route[%d]: empty address in %q", i, s)
		}
		if allDigits(rest) {
			rest = "127.0.0.1:" + rest
		} else if rest[0] == ':' {
			rest = "127.0.0.1" + rest
		}
		out = append(out, etunnel.Route{PathPrefix: prefix, Addr: rest, StripPrefix: strip})
	}
	// Delegate duplicate/shape validation to the SDK (one source of truth);
	// surface its error verbatim.
	if err := etunnel.ValidateRoutes(out); err != nil {
		return nil, err
	}
	return out, nil
}
