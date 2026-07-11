package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/IsuruSh/linkr/internal/domain"
	"github.com/IsuruSh/linkr/internal/httpx"
)

// Limiter is satisfied by *cache.RedisCache. Declared here, in the consumer, so
// the middleware does not import the cache package's concrete type and so tests
// can inject a fake — the same shape as TokenVerifier.
type Limiter interface {
	// Allow reports whether key is under limit for the window, and how long until
	// it resets. An error means the limiter itself is unreachable.
	Allow(ctx context.Context, key string, limit int, window time.Duration) (allowed bool, retryAfter time.Duration, err error)
}

// The limiter runs on a short budget of its own, NOT the request's context. A
// Redis outage must cost a few hundred milliseconds and then fail open — if it
// borrowed the request timeout, a stalled Redis would drain the whole budget and
// the fail-open request would then error on a starved downstream call.
const (
	rateLimitWindow  = time.Minute
	rateLimitTimeout = 250 * time.Millisecond
)

// keyFunc derives the rate-limit key from a request, or returns an error to fail
// closed (e.g. no authenticated user where one is required).
type keyFunc func(r *http.Request) (string, error)

// RateLimitByUser limits requests per authenticated user. Mounted inside the
// protected group, after RequireAuth, so the user ID is in context.
func RateLimitByUser(limiter Limiter, keyspace string, perMinute int, logger *slog.Logger) func(http.Handler) http.Handler {
	return rateLimit(limiter, keyspace, perMinute, logger, func(r *http.Request) (string, error) {
		userID, err := UserIDFrom(r.Context())
		if err != nil {
			// Should be impossible after RequireAuth, but fail closed rather than
			// key the limit on the nil user.
			return "", err
		}
		return userID.String(), nil
	})
}

// RateLimitByIP limits requests per client IP. It guards the unauthenticated
// auth endpoints, where there is no user to key on, against brute force and
// credential stuffing. See ClientIP for the trustProxy caveat.
func RateLimitByIP(limiter Limiter, keyspace string, perMinute int, trustProxy bool, logger *slog.Logger) func(http.Handler) http.Handler {
	return rateLimit(limiter, keyspace, perMinute, logger, func(r *http.Request) (string, error) {
		return ClientIP(r, trustProxy), nil
	})
}

// rateLimit is the shared core: derive the key, consult the limiter on a bounded
// budget, and either pass through, fail open on limiter error, or 429.
//
// Fail-open is deliberate: rate limiting is abuse control, not correctness, and
// taking a route offline because the cache blinked is a worse outage than the
// burst it would have stopped. This mirrors the redirect's fail-to-Postgres
// posture — both degrade toward availability.
func rateLimit(limiter Limiter, keyspace string, perMinute int, logger *slog.Logger, key keyFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := key(r)
			if err != nil {
				httpx.Error(w, r, err)
				return
			}

			ctx, cancel := context.WithTimeout(r.Context(), rateLimitTimeout)
			defer cancel()

			allowed, retryAfter, err := limiter.Allow(ctx, keyspace+":"+id, perMinute, rateLimitWindow)
			if err != nil {
				logger.WarnContext(r.Context(), "rate limiter unavailable, allowing request",
					"error", err, "keyspace", keyspace)
				next.ServeHTTP(w, r)
				return
			}

			if !allowed {
				writeRateLimited(w, r, retryAfter)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// writeRateLimited emits 429 with a Retry-After header. The remainder is rounded
// up so a sub-second wait is never reported as 0 while the caller is still limited.
func writeRateLimited(w http.ResponseWriter, r *http.Request, retryAfter time.Duration) {
	seconds := int(retryAfter.Seconds())
	if retryAfter%time.Second > 0 {
		seconds++
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	httpx.Error(w, r, domain.NewError(domain.CodeRateLimited,
		"rate limit exceeded, please try again shortly").
		WithDetails(map[string]string{"retry_after_seconds": strconv.Itoa(seconds)}))
}
