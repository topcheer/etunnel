package server

// Redis-backed global Counter: makes the per-IP and per-account tunnel
// limits exact across every instance. Enabled with ETUNNEL_REDIS_URL
// (redis://host:port[/db]); without it the in-memory counter is used and the
// limits stay per-instance (the documented single-node behaviour).

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

type redisCounter struct {
	rdb *redis.Client
}

func newRedisCounter(raw string) (*redisCounter, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("etunnel: bad ETUNNEL_REDIS_URL %q: %w", raw, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("etunnel: bad ETUNNEL_REDIS_URL %q (want redis://host:port[/db])", raw)
	}
	db := 0
	if p := u.Path; len(p) > 1 {
		db, err = strconv.Atoi(p[1:])
		if err != nil {
			return nil, fmt.Errorf("etunnel: bad redis db in %q", raw)
		}
	}
	opts := &redis.Options{Addr: u.Host, DB: db}
	if u.User != nil {
		if pw, ok := u.User.Password(); ok {
			opts.Password = pw
		}
	}
	rdb := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("etunnel: redis ping: %w", err)
	}
	return &redisCounter{rdb: rdb}, nil
}

func (c *redisCounter) Incr(ctx context.Context, key string, delta int64) (int64, error) {
	// INCRBY is atomic and returns the post-increment value.
	return c.rdb.IncrBy(ctx, "et:"+key, delta).Result()
}

// decrDel is the atomic decrement used by Decr: it removes the key once the
// count reaches zero (or below, which only a double release can produce) so a
// released quota leaves no zero-valued - or negative - residue behind
// (issue #36).
var decrDel = redis.NewScript(`
local v = redis.call('INCRBY', KEYS[1], -1)
if v <= 0 then redis.call('DEL', KEYS[1]) end
return v`)

func (c *redisCounter) Decr(ctx context.Context, key string) error {
	return decrDel.Run(ctx, c.rdb, []string{"et:" + key}).Err()
}

func (c *redisCounter) Get(ctx context.Context, key string) (int64, error) {
	v, err := c.rdb.Get(ctx, "et:"+key).Result()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

func (c *redisCounter) SetTTL(ctx context.Context, key string, d time.Duration) error {
	k := "et:" + key
	if d <= 0 {
		// PERSIST removes the expiry; per-account keys live until decremented.
		return c.rdb.Persist(ctx, k).Err()
	}
	return c.rdb.Expire(ctx, k, d).Err()
}

// Close releases the connection pool.
func (c *redisCounter) Close() error { return c.rdb.Close() }
