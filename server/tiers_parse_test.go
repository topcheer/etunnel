package server

// #196: tiers files decode strictly - a typo'd key (the README used to
// document ttlHours/bandwidthMbps while the Tier tags are ttl/bandwidth)
// must abort startup, not silently disable tier limits.

import (
	"testing"
	"time"
)

func TestParseTiersRejectsUnknownKeys(t *testing.T) {
	bad := []byte(`{"pro": {"name": "Pro", "ttlHours": 72, "customDomains": 3, "bandwidthMbps": 10}}`)
	if _, err := ParseTiers(bad); err == nil {
		t.Fatal("unknown key ttlHours must be rejected (#196)")
	}
	bad2 := []byte(`{"pro": {"name": "Pro", "ttl": 3600000000000, "bandwidth_mbps": 10}}`)
	if _, err := ParseTiers(bad2); err == nil {
		t.Fatal("unknown key bandwidth_mbps must be rejected (#196)")
	}
}

func TestParseTiersAcceptsKnownKeys(t *testing.T) {
	good := []byte(`{"pro": {"name": "Pro", "ttl": 14400000000000, "customDomains": 3, "bandwidth": 10000000, "maxTunnels": 5}}`)
	tiers, err := ParseTiers(good)
	if err != nil {
		t.Fatalf("known keys must parse: %v", err)
	}
	pro, ok := tiers["pro"]
	if !ok {
		t.Fatal("tier pro missing")
	}
	if pro.TTL != 4*time.Hour || pro.CustomDomains != 3 || pro.Bandwidth != 10000000 || pro.MaxTunnels != 5 {
		t.Fatalf("tier values wrong: %+v", pro)
	}
}
