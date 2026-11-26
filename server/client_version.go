package server

import (
	"fmt"
	"log"
	"strconv"
	"strings"
)

// Version is the server's own semver ("1.7.40"), injected at build time by
// the Makefile / Dockerfile / release CI with
//
//	go build -ldflags "-X github.com/topcheer/etunnel/server.Version=$(cat VERSION)"
//
// The zero value "dev" (plain `go build`) is not a semver, which switches the
// client version policy off - local dev builds and tests never reject anyone.
var Version = "dev"

// Defaults for Config.ClientVersionLag / Config.ClientDownloadURL.
const (
	defaultClientVersionLag  = 5
	defaultClientDownloadURL = "https://etun.uk" // product site; carries the SDK table and install commands
)

// ClientVersionPolicy is the hello.version gate: an SDK may lag at most
// MaxLag patches behind the server within the same major.minor; anything
// older (or version-less, or unparseable) is refused with
// protocol.ErrCodeClientOutdated and a message that names the download URL.
//
// Rules (S = server, C = client):
//   - C missing/unparseable                         -> reject
//   - C.major != S.major                            -> reject unless C.major > S.major
//   - C.minor != S.minor                            -> reject unless C.minor > S.minor
//   - same major.minor and S.patch - C.patch > lag  -> reject
//
// Everything else (same version, client newer, within the lag) is allowed.
type ClientVersionPolicy struct {
	Enabled     bool
	MaxLag      int
	DownloadURL string
}

// newClientVersionPolicy builds the policy from the config knobs. maxLag < 0
// disables it; a non-semver server Version disables it too (and says so once,
// so an operator who forgot the ldflags injection sees why in the log).
func newClientVersionPolicy(maxLag int, downloadURL string) ClientVersionPolicy {
	p := ClientVersionPolicy{MaxLag: maxLag, DownloadURL: downloadURL}
	if p.DownloadURL == "" {
		p.DownloadURL = defaultClientDownloadURL
	}
	if maxLag < 0 {
		return p
	}
	if _, ok := parseSemver(Version); !ok {
		log.Printf("etunneld: client version policy disabled: server version %q is not semver (build without -X server.Version?)", Version)
		return p
	}
	p.Enabled = true
	return p
}

// String renders the policy for the startup log.
func (p ClientVersionPolicy) String() string {
	if !p.Enabled {
		return fmt.Sprintf("disabled (lag %d)", p.MaxLag)
	}
	return fmt.Sprintf("enabled (max lag %d, SDK >= %s, download %s)", p.MaxLag, p.floorVersion(), p.DownloadURL)
}

// floorVersion is the oldest SDK the enabled policy admits.
func (p ClientVersionPolicy) floorVersion() string {
	s, ok := parseSemver(Version)
	if !ok {
		return ""
	}
	patch := s[2] - p.MaxLag
	if patch < 0 {
		patch = 0
	}
	return fmt.Sprintf("%d.%d.%d", s[0], s[1], patch)
}

// Check returns nil when a client announcing clientVer may connect, else the
// error text sent in the client_outdated error frame.
func (p ClientVersionPolicy) Check(clientVer string) error {
	if !p.Enabled {
		return nil
	}
	s, ok := parseSemver(Version)
	if !ok {
		return nil
	}
	c, ok := parseSemver(clientVer)
	if !ok {
		return fmt.Errorf("etunnel client did not report a valid version; this server runs %s and requires SDK >= %s. Upgrade: %s",
			Version, p.floorVersion(), p.DownloadURL)
	}
	tooOld := c[0] != s[0] && c[0] < s[0] ||
		c[0] == s[0] && c[1] != s[1] && c[1] < s[1] ||
		c[0] == s[0] && c[1] == s[1] && s[2]-c[2] > p.MaxLag
	if tooOld {
		return fmt.Errorf("etunnel client %s is too old; this server runs %s and requires SDK >= %s. Upgrade: %s",
			clientVer, Version, p.floorVersion(), p.DownloadURL)
	}
	return nil
}

// parseSemver parses "major.minor.patch" into three ints. A missing minor or
// patch counts as 0; a leading "v" and a "-pre"/"+build" suffix are tolerated;
// anything else (empty, "dev", "1.x", "1.7.") reports !ok.
func parseSemver(s string) ([3]int, bool) {
	var out [3]int
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return [3]int{}, false
	}
	for i, p := range parts {
		if p == "" {
			return [3]int{}, false
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}
