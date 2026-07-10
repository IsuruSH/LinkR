package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// keyPrefix is versioned. If Entry ever gains a field, bumping v1 to v2 rolls
// the whole namespace rather than trying to decode old values with a new shape.
const keyPrefix = "linkr:link:v1:"

// negativeSentinel marks "this code does not exist". An empty string is a value
// Redis stores happily and that Entry can never legitimately serialize to, so
// there is no ambiguity between "cached absent" and "cached present".
const negativeSentinel = ""

type RedisCache struct {
	client *redis.Client
}

var _ Cache = (*RedisCache)(nil)

// NewRedis parses a redis:// URL and verifies the connection eagerly, so a
// typo in REDIS_URL fails at boot instead of on the first redirect.
func NewRedis(ctx context.Context, url string) (*RedisCache, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parsing REDIS_URL: %w", err)
	}

	client := redis.NewClient(opts)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("connecting to redis: %w", err)
	}
	return &RedisCache{client: client}, nil
}

func key(code string) string { return keyPrefix + code }

func (c *RedisCache) GetLink(ctx context.Context, code string) (Entry, Lookup, error) {
	val, err := c.client.Get(ctx, key(code)).Result()

	switch {
	case errors.Is(err, redis.Nil):
		return Entry{}, Miss, nil
	case err != nil:
		return Entry{}, Miss, fmt.Errorf("redis get %q: %w", code, err)
	}

	if val == negativeSentinel {
		return Entry{}, Negative, nil
	}

	var e Entry
	if err := json.Unmarshal([]byte(val), &e); err != nil {
		// A corrupt or old-shaped value must not wedge the hot path. Drop it and
		// report a miss; the caller refills from Postgres.
		_ = c.client.Del(ctx, key(code)).Err()
		return Entry{}, Miss, nil
	}
	return e, Hit, nil
}

func (c *RedisCache) SetLink(ctx context.Context, code string, e Entry, ttl time.Duration) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshaling cache entry: %w", err)
	}
	return c.client.Set(ctx, key(code), payload, ttl).Err()
}

func (c *RedisCache) SetMissing(ctx context.Context, code string, ttl time.Duration) error {
	return c.client.Set(ctx, key(code), negativeSentinel, ttl).Err()
}

func (c *RedisCache) Invalidate(ctx context.Context, code string) error {
	return c.client.Del(ctx, key(code)).Err()
}

func (c *RedisCache) Ping(ctx context.Context) error {
	return c.client.Ping(ctx).Err()
}

func (c *RedisCache) Close() error { return c.client.Close() }
