package handler

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/IsuruSh/linkr/internal/httpx"
)

const probeTimeout = 2 * time.Second

// Pinger is any dependency that can be probed. Satisfied by *pgxpool.Pool and
// *cache.RedisCache.
type Pinger interface {
	Ping(ctx context.Context) error
}

type HealthHandler struct {
	db     Pinger
	cache  Pinger
	logger *slog.Logger
}

// The probes get no Routes() method. They live at the root, where orchestrators
// expect them, and mounting a sub-router at "/" would collide with the redirect
// at "/{code}". main.go registers them directly.
func NewHealthHandler(db, cache Pinger, logger *slog.Logger) *HealthHandler {
	return &HealthHandler{db: db, cache: cache, logger: logger}
}

// Live answers "is the process up". It touches no dependency on purpose: if it
// probed Postgres, a database blip would make the orchestrator kill every
// healthy replica, and restarting cannot fix someone else's database.
func (h *HealthHandler) Live(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Ready answers "can I serve traffic". A failing probe pulls the replica out of
// the load balancer without killing it.
//
// Postgres down is fatal to readiness. Redis down is not: the redirect degrades
// to a Postgres read and keeps working, so we report "degraded" and stay in
// rotation rather than taking the service offline over a cache outage.
func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()

	// The two probes are independent network round-trips, so overlap them: the
	// endpoint's latency becomes max(postgres, redis) instead of their sum, which
	// matters when a dependency is slow-but-alive and an orchestrator is polling
	// this every few seconds. Race-free by construction — each goroutine writes
	// its own variable and wg.Wait happens-before the reads.
	var wg sync.WaitGroup
	var dbErr, cacheErr error
	wg.Add(2)
	go func() { defer wg.Done(); dbErr = h.db.Ping(ctx) }()
	go func() { defer wg.Done(); cacheErr = h.cache.Ping(ctx) }()
	wg.Wait()

	checks := map[string]string{"postgres": "ok", "redis": "ok"}
	status := http.StatusOK

	if dbErr != nil {
		checks["postgres"] = "unreachable"
		status = http.StatusServiceUnavailable
		h.logger.WarnContext(ctx, "readiness probe failed", "dependency", "postgres", "error", dbErr)
	}
	if cacheErr != nil {
		checks["redis"] = "unreachable"
		h.logger.WarnContext(ctx, "cache probe failed, redirects fall back to postgres", "error", cacheErr)
	}

	httpx.JSON(w, status, map[string]any{
		"status": readyState(status, checks["redis"]),
		"checks": checks,
	})
}

func readyState(status int, redis string) string {
	switch {
	case status != http.StatusOK:
		return "unavailable"
	case redis != "ok":
		return "degraded"
	default:
		return "ready"
	}
}
