package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// rateLimitPrefix is versioned like the link cache, so the namespace can be
// rolled without colliding with old keys.
const rateLimitPrefix = "linkr:ratelimit:v1:"

// allowScript is a fixed-window counter, run atomically on the server.
//
// INCR then EXPIRE as two round trips has a real failure: if the process dies
// between them, the key has no TTL and that caller is limited forever. Doing
// both in one script closes that — and the EXPIRE is set only on the first hit
// (count == 1), so a burst cannot keep pushing the window's reset forward.
//
// KEYS[1] = counter key   ARGV[1] = limit   ARGV[2] = window seconds
// Returns { allowed (1|0), retry_after_seconds }.
var allowScript = redis.NewScript(`
local count = redis.call("INCR", KEYS[1])
if count == 1 then
	redis.call("EXPIRE", KEYS[1], ARGV[2])
end
local ttl = redis.call("TTL", KEYS[1])
if ttl < 0 then
	-- No TTL somehow (e.g. key set by another path). Repair it rather than
	-- leaving a key that never resets.
	redis.call("EXPIRE", KEYS[1], ARGV[2])
	ttl = tonumber(ARGV[2])
end
if count > tonumber(ARGV[1]) then
	return {0, ttl}
end
return {1, ttl}
`)

// Allow reports whether key is under limit for the current window, and — when it
// is not — how long until the window resets, for a Retry-After header.
//
// It runs one Lua script server-side, so the increment, the expiry, and the
// comparison are a single atomic operation and a single round trip.
func (c *RedisCache) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	res, err := allowScript.Run(ctx, c.client,
		[]string{rateLimitPrefix + key},
		limit, int(window.Seconds()),
	).Result()
	if err != nil {
		return false, 0, fmt.Errorf("rate limit script: %w", err)
	}

	vals, ok := res.([]any)
	if !ok || len(vals) != 2 {
		return false, 0, errors.New("rate limit script: unexpected reply shape")
	}

	allowed, _ := vals[0].(int64)
	ttlSeconds, _ := vals[1].(int64)

	return allowed == 1, time.Duration(ttlSeconds) * time.Second, nil
}
