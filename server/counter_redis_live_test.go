package server

// Live test against the Redis on host 13 (audiobook-redis, db 15 to avoid
// touching the audiobook data). Skipped when 13 is unreachable so CI on
// GitHub does not fail.

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestRedisCounterLive(t *testing.T) {
	redisURL := os.Getenv("ET_REDIS_TEST_URL")
	if redisURL == "" {
		t.Skip("ET_REDIS_TEST_URL not set (live Redis test)")
	}
	c, err := newRedisCounter(redisURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()
	ctx := context.Background()
	// cleanup then incr/decr roundtrip
	c.rdb.Del(ctx, "et:test:ip")
	v, err := c.Incr(ctx, "test:ip", 1)
	if err != nil || v != 1 {
		t.Fatalf("Incr -> %d %v", v, err)
	}
	v, _ = c.Incr(ctx, "test:ip", 1)
	if v != 2 {
		t.Fatalf("second Incr -> %d", v)
	}
	if err := c.Decr(ctx, "test:ip"); err != nil {
		t.Fatal(err)
	}
	if v, _ := c.Get(ctx, "test:ip"); v != 1 {
		t.Fatalf("Get after Decr -> %d", v)
	}
	// TTL
	if err := c.SetTTL(ctx, "test:ip", 1100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1300 * time.Millisecond)
	if v, _ := c.Get(ctx, "test:ip"); v != 0 {
		t.Fatalf("expired -> %d", v)
	}
	// never
	c.Incr(ctx, "test:acct", 3)
	c.SetTTL(ctx, "test:acct", TTLNever)
	time.Sleep(30 * time.Millisecond)
	if v, _ := c.Get(ctx, "test:acct"); v != 3 {
		t.Fatalf("TTLNever -> %d", v)
	}
	c.rdb.Del(ctx, "et:test:acct")
}
