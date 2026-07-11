package middleware

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeLimiter counts calls per key in-process, so the middleware can be tested
// without Redis. It mimics the fixed-window contract: over the limit returns
// allowed=false and a retry-after.
type fakeLimiter struct {
	mu       sync.Mutex
	counts   map[string]int
	limit    int
	err      error // when set, every Allow returns it (simulates Redis down)
	retryFor time.Duration
	calls    int
}

func newFakeLimiter(limit int) *fakeLimiter {
	return &fakeLimiter{counts: map[string]int{}, limit: limit, retryFor: 30 * time.Second}
}

func (f *fakeLimiter) Allow(_ context.Context, key string, limit int, _ time.Duration) (bool, time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return false, 0, f.err
	}
	f.counts[key]++
	return f.counts[key] <= limit, f.retryFor, nil
}

// okHandler is the thing the middleware wraps; it records whether it ran.
func okHandler(ran *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*ran = true
		w.WriteHeader(http.StatusCreated)
	})
}

func requestAs(userID uuid.UUID) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/links", nil)
	return req.WithContext(WithUserID(req.Context(), userID))
}

func TestRateLimit_AllowsUpToLimitThenBlocks(t *testing.T) {
	t.Parallel()

	limiter := newFakeLimiter(3)
	user := uuid.New()
	mw := RateLimitByUser(limiter, "create", 3, discardLogger())

	for i := 1; i <= 3; i++ {
		var ran bool
		rec := httptest.NewRecorder()
		mw(okHandler(&ran)).ServeHTTP(rec, requestAs(user))
		if !ran || rec.Code != http.StatusCreated {
			t.Fatalf("request %d: blocked at or below the limit (code %d)", i, rec.Code)
		}
	}

	// The 4th is over the limit.
	var ran bool
	rec := httptest.NewRecorder()
	mw(okHandler(&ran)).ServeHTTP(rec, requestAs(user))

	if ran {
		t.Error("handler ran on a request that should have been rate-limited")
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Error("no Retry-After header on a 429")
	}
}

func TestRateLimit_IsPerUser(t *testing.T) {
	t.Parallel()

	limiter := newFakeLimiter(1)
	mw := RateLimitByUser(limiter, "create", 1, discardLogger())

	userA, userB := uuid.New(), uuid.New()

	// A spends the single allowance.
	var ranA bool
	recA := httptest.NewRecorder()
	mw(okHandler(&ranA)).ServeHTTP(recA, requestAs(userA))
	if recA.Code != http.StatusCreated {
		t.Fatalf("user A first request = %d, want 201", recA.Code)
	}

	// A is now blocked...
	recA2 := httptest.NewRecorder()
	mw(okHandler(new(bool))).ServeHTTP(recA2, requestAs(userA))
	if recA2.Code != http.StatusTooManyRequests {
		t.Fatalf("user A second request = %d, want 429", recA2.Code)
	}

	// ...but B is unaffected. This is the property a shared/global counter breaks.
	var ranB bool
	recB := httptest.NewRecorder()
	mw(okHandler(&ranB)).ServeHTTP(recB, requestAs(userB))
	if !ranB || recB.Code != http.StatusCreated {
		t.Fatalf("user B request = %d, want 201: one user's spending must not limit another", recB.Code)
	}
}

// The failure the design turns on: if the limiter errors (Redis down), the
// request is allowed, not blocked.
func TestRateLimit_FailsOpenWhenLimiterErrors(t *testing.T) {
	t.Parallel()

	limiter := newFakeLimiter(1)
	limiter.err = errors.New("dial tcp 127.0.0.1:6379: connect: connection refused")

	mw := RateLimitByUser(limiter, "create", 1, discardLogger())

	var ran bool
	rec := httptest.NewRecorder()
	mw(okHandler(&ran)).ServeHTTP(rec, requestAs(uuid.New()))

	if !ran {
		t.Error("handler did not run: rate limiting must fail open, not take creation offline")
	}
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201 (fail-open)", rec.Code)
	}
}

// Without a user in context (RequireAuth forgotten upstream) it must fail closed,
// not key the limit on the nil user.
func TestRateLimit_FailsClosedWithoutUser(t *testing.T) {
	t.Parallel()

	limiter := newFakeLimiter(10)
	mw := RateLimitByUser(limiter, "create", 10, discardLogger())

	var ran bool
	rec := httptest.NewRecorder()
	// No WithUserID on the context.
	mw(okHandler(&ran)).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/links", nil))

	if ran {
		t.Error("handler ran without an authenticated user")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if limiter.calls != 0 {
		t.Error("limiter was consulted before establishing the user")
	}
}

// RateLimitByIP keys on the client IP, so two IPs are limited independently and
// XFF is only honoured when the proxy is trusted.
func TestRateLimitByIP_KeysOnClientIP(t *testing.T) {
	t.Parallel()

	limiter := newFakeLimiter(1)
	mw := RateLimitByIP(limiter, "auth", 1, false, discardLogger())

	reqFrom := func(addr string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
		req.RemoteAddr = addr + ":5000"
		return req
	}

	// First request from IP A passes, second is limited.
	rec := httptest.NewRecorder()
	mw(okHandler(new(bool))).ServeHTTP(rec, reqFrom("10.0.0.1"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("A first = %d, want 201", rec.Code)
	}
	rec = httptest.NewRecorder()
	mw(okHandler(new(bool))).ServeHTTP(rec, reqFrom("10.0.0.1"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("A second = %d, want 429", rec.Code)
	}

	// A different IP is unaffected.
	var ranB bool
	rec = httptest.NewRecorder()
	mw(okHandler(&ranB)).ServeHTTP(rec, reqFrom("10.0.0.2"))
	if !ranB || rec.Code != http.StatusCreated {
		t.Fatalf("B = %d, want 201: a different IP must have its own budget", rec.Code)
	}
}

// Retry-After rounds a sub-second remainder up, so it is never reported as 0
// while the caller is still limited.
func TestRateLimit_RetryAfterRoundsUp(t *testing.T) {
	t.Parallel()

	limiter := newFakeLimiter(0) // limit 0 => always blocked
	limiter.retryFor = 1500 * time.Millisecond
	mw := RateLimitByUser(limiter, "create", 0, discardLogger())

	rec := httptest.NewRecorder()
	mw(okHandler(new(bool))).ServeHTTP(rec, requestAs(uuid.New()))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Errorf("Retry-After = %q, want \"2\" (1.5s rounded up)", got)
	}
}
