package protocol

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

var adjectives = []string{
	"amber", "atomic", "autumn", "azure", "bitter", "blazing", "brave", "breezy",
	"calm", "capable", "charmed", "clever", "cobalt", "cosmic", "crimson", "crisp",
	"dapper", "daring", "dawn", "divine", "eager", "elder", "ember", "endless",
	"fabled", "fancy", "ferny", "flint", "flying", "frosty", "gentle", "gilded",
	"happy", "hidden", "hollow", "ionic", "ivory", "jolly", "keen", "lacy",
	"lucky", "misty", "nimble", "noble", "olive", "opal", "rapid", "quiet",
}

var nouns = []string{
	"auroch", "badger", "beacon", "bison", "bobcat", "bronco", "cactus", "cascade",
	"cattle", "cheetah", "cherry", "cinder", "condor", "copper", "cougar", "coyote",
	"crane", "cricket", "dolphin", "dragon", "egret", "falcon", "ferret", "flamingo",
	"gazelle", "gecko", "gopher", "harbor", "heron", "ibex", "impala", "jaguar",
	"juniper", "kestrel", "lagoon", "lemur", "linx", "llama", "magenta", "marten",
	"meadow", "muffin", "narwhal", "ocelot", "onyx", "osprey", "otter", "puffin",
}

var reservedSubdomains = map[string]bool{
	"www": true, "api": true, "admin": true, "mail": true, "smtp": true, "pop": true,
	"imap": true, "ftp": true, "ns1": true, "ns2": true, "mx": true, "mx1": true,
	"mx2": true, "localhost": true, "tunnel": true, "tunnels": true, "dashboard": true,
	"status": true, "static": true, "docs": true, "doc": true, "app": true, "apps": true,
	"blog": true, "shop": true, "store": true, "dev": true, "staging": true, "prod": true,
	"support": true, "help": true, "cdn": true, "assets": true, "media": true,
	"auth": true, "login": true, "sso": true, "billing": true, "pay": true,
	"register": true, "account": true, "accounts": true, "metrics": true, "stats": true,
}

// ErrBadSubdomain is returned by ValidateSubdomain.
var ErrBadSubdomain = errors.New("etunnel: invalid subdomain")

// RandomSubdomain generates a human-readable random subdomain such as
// "quick-fox-4821".
//
// The four-digit suffix keeps the namespace large enough that random draws
// rarely collide: 46 adjectives x 47 nouns x 10000 = ~21.6M names, against
// ~216k with the previous two-digit suffix (issue #14).
func RandomSubdomain() string {
	a := adjectives[randInt(len(adjectives))]
	n := nouns[randInt(len(nouns))]
	return fmt.Sprintf("%s-%s-%04d", a, n, randInt(subdomainSuffixSpace))
}

// subdomainSuffixSpace is the numeric suffix space of RandomSubdomain.
const subdomainSuffixSpace = 10000

func randInt(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

// NormalizeSubdomain trims and lowercases a requested subdomain.
func NormalizeSubdomain(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// ValidateSubdomain checks that a subdomain is a single DNS label usable as a
// tunnel hostname: 3-40 chars, [a-z0-9-], no leading/trailing hyphen, not a
// reserved word.
func ValidateSubdomain(s string) error {
	if len(s) < 3 || len(s) > 40 {
		return fmt.Errorf("%w: must be 3-40 characters", ErrBadSubdomain)
	}
	if s[0] == '-' || s[len(s)-1] == '-' {
		return fmt.Errorf("%w: cannot start or end with a hyphen", ErrBadSubdomain)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return fmt.Errorf("%w: only a-z, 0-9 and hyphen allowed", ErrBadSubdomain)
		}
	}
	if reservedSubdomains[s] {
		return fmt.Errorf("%w: %q is reserved", ErrBadSubdomain, s)
	}
	return nil
}
