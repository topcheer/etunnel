package server

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestMemCounterBasic(t *testing.T) {
	c := newMemCounter()
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		if v, _ := c.Incr(ctx, "ip:1.2.3.4", 1); v != int64(i) {
			t.Fatalf("Incr -> %d, want %d", v, i)
		}
	}
	if v, _ := c.Get(ctx, "ip:1.2.3.4"); v != 3 {
		t.Fatalf("Get -> %d", v)
	}
	_ = c.Decr(ctx, "ip:1.2.3.4")
	if v, _ := c.Get(ctx, "ip:1.2.3.4"); v != 2 {
		t.Fatalf("after Decr -> %d", v)
	}
}

func TestMemCounterTTL(t *testing.T) {
	c := newMemCounter()
	ctx := context.Background()
	c.Incr(ctx, "k", 5)
	_ = c.SetTTL(ctx, "k", 20*time.Millisecond)
	if v, _ := c.Get(ctx, "k"); v != 5 {
		t.Fatal("before expiry")
	}
	time.Sleep(30 * time.Millisecond)
	if v, _ := c.Get(ctx, "k"); v != 0 {
		t.Fatal("expired key must read 0")
	}
	// never-expiring keys survive
	c.Incr(ctx, "acct:k", 1)
	_ = c.SetTTL(ctx, "acct:k", TTLNever)
	time.Sleep(5 * time.Millisecond)
	if v, _ := c.Get(ctx, "acct:k"); v != 1 {
		t.Fatal("TTLNever key expired")
	}
}

func TestMemCounterConcurrent(t *testing.T) {
	c := newMemCounter()
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.Incr(ctx, "hot", 1) }()
	}
	wg.Wait()
	if v, _ := c.Get(ctx, "hot"); v != 50 {
		t.Fatalf("concurrent Incr -> %d, want 50", v)
	}
}
