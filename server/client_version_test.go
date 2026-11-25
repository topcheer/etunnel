package server

import (
	"strings"
	"testing"
)

// setServerVersion pins the package-level build version for a test.
func setServerVersion(t *testing.T, v string) {
	t.Helper()
	old := Version
	Version = v
	t.Cleanup(func() { Version = old })
}

func TestParseSemver(t *testing.T) {
	cases := map[string]struct {
		want [3]int
		ok   bool
	}{
		"1.7.40":     {[3]int{1, 7, 40}, true},
		"1.7":        {[3]int{1, 7, 0}, true}, // missing patch == 0
		"1":          {[3]int{1, 0, 0}, true},
		"v1.7.40":    {[3]int{1, 7, 40}, true}, // tolerate a leading v
		"1.7.40-rc1": {[3]int{1, 7, 40}, true}, // pre-release/build metadata ignored
		"1.7.40+b3":  {[3]int{1, 7, 40}, true},
		"":           {[3]int{}, false},
		"dev":        {[3]int{}, false},
		"1.x":        {[3]int{}, false},
		"a.b.c":      {[3]int{}, false},
		"1.7.":       {[3]int{}, false},
	}
	for in, tc := range cases {
		got, ok := parseSemver(in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseSemver(%q) = %v,%v want %v,%v", in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestFloorVersion(t *testing.T) {
	cases := map[string]string{"1.7.40": "1.7.35", "1.7.3": "1.7.0", "2.0.0": "2.0.0", "dev": "", "1.7": "1.7.0"}
	for sv, want := range cases {
		setServerVersion(t, sv)
		p := ClientVersionPolicy{Enabled: true, MaxLag: 5}
		if got := p.floorVersion(); got != want {
			t.Errorf("floorVersion(server %s) = %q want %q", sv, got, want)
		}
	}
}

// "Allowed to lag five patches": the whole contract in one table.
func TestClientVersionPolicyCheck(t *testing.T) {
	setServerVersion(t, "1.7.40")
	const url = "https://dl.example/etunnel"
	p := ClientVersionPolicy{Enabled: true, MaxLag: 5, DownloadURL: url}

	reject := func(ver, wantSub string) {
		t.Helper()
		err := p.Check(ver)
		if err == nil {
			t.Fatalf("Check(%q): want rejection", ver)
		}
		msg := err.Error()
		for _, sub := range []string{wantSub, "this server runs 1.7.40", "requires SDK >= 1.7.35", "Upgrade: " + url} {
			if !strings.Contains(msg, sub) {
				t.Errorf("Check(%q) message %q misses %q", ver, msg, sub)
			}
		}
	}
	allow := func(ver string) {
		t.Helper()
		if err := p.Check(ver); err != nil {
			t.Fatalf("Check(%q): want allowed, got %v", ver, err)
		}
	}

	reject("", "did not report a valid version")
	reject("garbage", "did not report a valid version")
	reject("0.9.0", "client 0.9.0 is too old")   // older major
	reject("1.6.99", "client 1.6.99 is too old") // older minor
	reject("1.7", "client 1.7 is too old")       // patch defaults to 0 -> lag 40
	reject("1.7.34", "client 1.7.34 is too old") // lag 6
	allow("1.7.35")                              // lag 5: the floor
	allow("1.7.40")                              // same
	allow("1.7.41")                              // client newer in the same minor
	allow("1.8.0")                               // client newer minor
	allow("2.0.0")                               // client newer major

	// Configurable threshold.
	p0 := ClientVersionPolicy{Enabled: true, MaxLag: 0, DownloadURL: url}
	if err := p0.Check("1.7.40"); err != nil {
		t.Errorf("lag 0, same version: want allowed, got %v", err)
	}
	if err := p0.Check("1.7.39"); err == nil {
		t.Errorf("lag 0, one patch behind: want rejection")
	}
	// Disabled policy never rejects.
	off := ClientVersionPolicy{Enabled: false, MaxLag: 5, DownloadURL: url}
	if err := off.Check("garbage"); err != nil {
		t.Errorf("disabled policy rejected: %v", err)
	}
}

// A dev build ("dev", empty, ...) cannot know its own version: the policy must
// switch itself off (and say so once) so local dev and the existing tests -
// whose clients never sent a version - keep working.
func TestClientVersionPolicyAutoDisabledOnDevServer(t *testing.T) {
	for _, sv := range []string{"dev", "", "garbage"} {
		setServerVersion(t, sv)
		p := newClientVersionPolicy(5, "https://dl.example")
		if p.Enabled {
			t.Errorf("server version %q: policy must be disabled", sv)
		}
		if err := p.Check("0.0.1"); err != nil {
			t.Errorf("server version %q: disabled policy rejected %v", sv, err)
		}
	}
	setServerVersion(t, "1.7.40")
	if p := newClientVersionPolicy(5, "https://dl.example"); !p.Enabled {
		t.Errorf("release version: policy must be enabled")
	}
	if p := newClientVersionPolicy(-1, "https://dl.example"); p.Enabled {
		t.Errorf("lag -1: policy must be disabled")
	}
}
