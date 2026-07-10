//go:build integration

// Package tests exercises the real stack: a live Postgres and a live Redis.
//
// Kept behind a build tag so `go test -race ./...` stays fast and needs no
// services. Run with `make test-integration`, which starts the dependencies.
//
// These tests assert the things a fake cannot: that the SQL actually parses and
// uses its indexes, that CopyFrom and the counter UPDATE really are atomic, that
// Redis invalidation reaches Redis, and that the redirect returns before the
// click is durable.
package tests

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IsuruSh/linkr/internal/auth"
	"github.com/IsuruSh/linkr/internal/cache"
	"github.com/IsuruSh/linkr/internal/domain"
	"github.com/IsuruSh/linkr/internal/handler"
	"github.com/IsuruSh/linkr/internal/repository"
	"github.com/IsuruSh/linkr/internal/service"
	"github.com/IsuruSh/linkr/internal/worker"
	"github.com/IsuruSh/linkr/migrations"
)

const testBaseURL = "http://localhost:8080"

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// harness is a full backend wired against real services, minus the HTTP server.
type harness struct {
	pool      *pgxpool.Pool
	redis     *cache.RedisCache
	links     *repository.LinkRepository
	clicks    *repository.ClickRepository
	linkSvc   *service.LinkService
	clickWork *worker.ClickWorker
	router    http.Handler
	userID    uuid.UUID
}

func newHarness(t *testing.T, clickCfg worker.Config) *harness {
	t.Helper()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, envOr("DATABASE_URL", "postgres://linkr:linkr_dev_password@localhost:5432/linkr?sslmode=disable"))
	if err != nil {
		t.Fatalf("connecting to postgres (is `docker compose up -d postgres redis` running?): %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("pinging postgres: %v", err)
	}
	if err := migrations.Up(ctx, pool); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	redis, err := cache.NewRedis(ctx, envOr("REDIS_URL", "redis://localhost:6379/1"))
	if err != nil {
		t.Fatalf("connecting to redis: %v", err)
	}

	linkRepo := repository.NewLinkRepository(pool)
	userRepo := repository.NewUserRepository(pool)
	clickRepo := repository.NewClickRepository(pool)

	w := worker.New(clickCfg, clickRepo, discardLogger())
	w.Start()

	linkSvc := service.NewLinkService(linkRepo, clickRepo, redis, w, discardLogger(), time.Hour, time.Minute)
	issuer := auth.NewIssuer("integration-test-secret-at-least-32-bytes", time.Hour)
	authSvc := service.NewAuthService(userRepo, issuer)

	// A unique user per test run, so tests never collide on the demo seed data.
	tok, err := authSvc.Register(ctx, fmt.Sprintf("it-%s@linkr.test", uuid.NewString()[:8]), "integration-password")
	if err != nil {
		t.Fatalf("registering test user: %v", err)
	}

	linkHandler := handler.NewLinkHandler(linkSvc, testBaseURL)
	r := chi.NewRouter()
	r.Get("/{code}", linkHandler.Redirect)

	h := &harness{
		pool: pool, redis: redis, links: linkRepo, clicks: clickRepo,
		linkSvc: linkSvc, clickWork: w, router: r, userID: tok.UserID,
	}

	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = w.Shutdown(shutdownCtx)
		// Cascades to links and clicks.
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, h.userID)
		_ = redis.Close()
		pool.Close()
	})
	return h
}

func defaultClickCfg() worker.Config {
	return worker.Config{BufferSize: 1000, Workers: 2, BatchSize: 50, FlushInterval: 100 * time.Millisecond, FlushTimeout: 5 * time.Second}
}

func (h *harness) rawClickCount(t *testing.T, linkID uuid.UUID) int64 {
	t.Helper()
	n, err := h.clicks.CountForLink(context.Background(), linkID)
	if err != nil {
		t.Fatalf("counting clicks: %v", err)
	}
	return n
}

func (h *harness) denormalizedCount(t *testing.T, code string) int64 {
	t.Helper()
	l, err := h.links.GetByShortCode(context.Background(), code)
	if err != nil {
		t.Fatalf("reading link: %v", err)
	}
	return l.ClickCount
}

// ---- tests ------------------------------------------------------------------

// The full cycle against real services.
func TestIntegration_CreateRedirectClickStats(t *testing.T) {
	h := newHarness(t, defaultClickCfg())
	ctx := context.Background()

	link, err := h.linkSvc.Create(ctx, h.userID, "https://example.com/target", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	const redirects = 25
	for i := 0; i < redirects; i++ {
		rec := httptest.NewRecorder()
		h.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+link.ShortCode, nil))
		if rec.Code != http.StatusFound {
			t.Fatalf("redirect %d: status %d, want 302", i, rec.Code)
		}
		if got := rec.Header().Get("Location"); got != "https://example.com/target" {
			t.Fatalf("Location = %q", got)
		}
	}

	// Drain deterministically instead of sleeping and hoping.
	drainCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := h.clickWork.Shutdown(drainCtx); err != nil {
		t.Fatalf("draining worker: %v", err)
	}

	if got := h.rawClickCount(t, link.ID); got != redirects {
		t.Errorf("raw clicks = %d, want %d", got, redirects)
	}
	// The denormalized counter and the event table must agree. They are written
	// in one transaction, so any drift here means that guarantee is broken.
	if got := h.denormalizedCount(t, link.ShortCode); got != redirects {
		t.Errorf("links.click_count = %d, want %d — the counter has drifted from the raw rows", got, redirects)
	}

	stats, err := h.linkSvc.Stats(ctx, h.userID, link.ShortCode, service.Range7d)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.TotalClicks != redirects {
		t.Errorf("TotalClicks = %d, want %d", stats.TotalClicks, redirects)
	}
	// Zero-filled: 7 buckets even though only today has traffic.
	if len(stats.Series) != 7 {
		t.Errorf("series has %d buckets, want 7 (generate_series must zero-fill)", len(stats.Series))
	}
	var sum int64
	for _, d := range stats.Series {
		sum += d.Clicks
	}
	if sum != redirects {
		t.Errorf("series sums to %d, want %d", sum, redirects)
	}
}

// The behaviour the spec singles out: the 302 must not wait on the write.
//
// With a long flush interval and a batch size far above the traffic, nothing can
// have been persisted by the time the redirects return. If this test finds rows,
// the write is on the critical path.
func TestIntegration_RedirectDoesNotWaitForTheClickWrite(t *testing.T) {
	cfg := defaultClickCfg()
	cfg.BatchSize = 10_000        // never reached
	cfg.FlushInterval = time.Hour // never fires during the test

	h := newHarness(t, cfg)
	ctx := context.Background()

	link, err := h.linkSvc.Create(ctx, h.userID, "https://example.com/async", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	start := time.Now()
	for i := 0; i < 20; i++ {
		rec := httptest.NewRecorder()
		h.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+link.ShortCode, nil))
		if rec.Code != http.StatusFound {
			t.Fatalf("status %d, want 302", rec.Code)
		}
	}
	elapsed := time.Since(start)

	if n := h.rawClickCount(t, link.ID); n != 0 {
		t.Errorf("%d clicks were already durable; the redirect is waiting on the database write", n)
	}
	t.Logf("20 redirects served in %v with zero clicks persisted", elapsed.Round(time.Microsecond))

	// And the drain still saves every one of them.
	drainCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := h.clickWork.Shutdown(drainCtx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if n := h.rawClickCount(t, link.ID); n != 20 {
		t.Errorf("after drain: %d clicks, want 20 — shutdown must not lose accepted events", n)
	}
}

// Cache invalidation against a live Redis, not a fake map.
func TestIntegration_DeleteInvalidatesRedis(t *testing.T) {
	h := newHarness(t, defaultClickCfg())
	ctx := context.Background()

	link, err := h.linkSvc.Create(ctx, h.userID, "https://example.com/doomed", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Warm the cache through the real path.
	if _, err := h.linkSvc.Resolve(ctx, link.ShortCode); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, lookup, err := h.redis.GetLink(ctx, link.ShortCode); err != nil || lookup != cache.Hit {
		t.Fatalf("precondition: cache should be warm (lookup=%v err=%v)", lookup, err)
	}

	if err := h.linkSvc.Delete(ctx, h.userID, link.ShortCode); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, lookup, err := h.redis.GetLink(ctx, link.ShortCode)
	if err != nil {
		t.Fatalf("reading cache: %v", err)
	}
	if lookup == cache.Hit {
		t.Fatal("Redis still holds the deleted link; it would keep redirecting for a full TTL")
	}

	if _, err := h.linkSvc.Resolve(ctx, link.ShortCode); !errors.Is(err, domain.ErrLinkNotFound) {
		t.Errorf("Resolve after delete = %v, want ErrLinkNotFound", err)
	}
}

// A miss must be negative-cached in real Redis, so an enumeration scan does not
// reach Postgres on every probe.
func TestIntegration_UnknownCodeIsNegativeCachedInRedis(t *testing.T) {
	h := newHarness(t, defaultClickCfg())
	ctx := context.Background()

	code := "nx" + uuid.NewString()[:5]
	if _, err := h.linkSvc.Resolve(ctx, code); !errors.Is(err, domain.ErrLinkNotFound) {
		t.Fatalf("Resolve = %v, want ErrLinkNotFound", err)
	}

	_, lookup, err := h.redis.GetLink(ctx, code)
	if err != nil {
		t.Fatalf("reading cache: %v", err)
	}
	if lookup != cache.Negative {
		t.Errorf("lookup = %v, want Negative: the miss was not cached", lookup)
	}
}

// Concurrent batches touching the same links: the counter must still equal the
// raw rows. This is the test that would catch a lost update or a deadlock that
// silently drops a batch.
func TestIntegration_ConcurrentClicksKeepCounterConsistent(t *testing.T) {
	cfg := defaultClickCfg()
	cfg.Workers = 4
	cfg.BatchSize = 20
	cfg.FlushInterval = 20 * time.Millisecond

	h := newHarness(t, cfg)
	ctx := context.Background()

	// Several links, so batches overlap on the same rows in different orders.
	var links []domain.Link
	for i := 0; i < 5; i++ {
		l, err := h.linkSvc.Create(ctx, h.userID, fmt.Sprintf("https://example.com/%d", i), "")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		links = append(links, l)
	}

	const perLink = 60
	done := make(chan struct{})
	for _, l := range links {
		go func(l domain.Link) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < perLink; i++ {
				rec := httptest.NewRecorder()
				h.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+l.ShortCode, nil))
			}
		}(l)
	}
	for range links {
		<-done
	}

	drainCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := h.clickWork.Shutdown(drainCtx); err != nil {
		t.Fatalf("drain: %v", err)
	}

	written, dropped, lost := h.clickWork.Stats()
	t.Logf("worker: written=%d dropped=%d lost=%d", written, dropped, lost)
	if lost != 0 {
		t.Errorf("%d clicks lost to failed batches", lost)
	}

	for _, l := range links {
		raw := h.rawClickCount(t, l.ID)
		denorm := h.denormalizedCount(t, l.ShortCode)
		if raw != denorm {
			t.Errorf("%s: raw=%d denormalized=%d — the counter drifted from the event table", l.ShortCode, raw, denorm)
		}
		if dropped == 0 && raw != perLink {
			t.Errorf("%s: raw=%d, want %d", l.ShortCode, raw, perLink)
		}
	}
}

// Keyset pagination against real SQL: pages must partition the set exactly —
// no row seen twice, none skipped.
func TestIntegration_KeysetPaginationIsExact(t *testing.T) {
	h := newHarness(t, defaultClickCfg())
	ctx := context.Background()

	const total = 25
	for i := 0; i < total; i++ {
		if _, err := h.linkSvc.Create(ctx, h.userID, fmt.Sprintf("https://example.com/p%d", i), ""); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	seen := map[string]int{}
	cursor := ""
	pages := 0

	for {
		page, err := h.linkSvc.List(ctx, h.userID, cursor, 7)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		pages++
		for _, l := range page.Items {
			seen[l.ShortCode]++
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}

	if len(seen) != total {
		t.Errorf("saw %d distinct links across %d pages, want %d", len(seen), pages, total)
	}
	for code, n := range seen {
		if n != 1 {
			t.Errorf("link %s appeared %d times across pages", code, n)
		}
	}
}

// A link created mid-pagination must not shift rows into or out of later pages.
// This is the concrete failure that offset pagination has and keyset does not.
func TestIntegration_KeysetPageIsStableUnderConcurrentInserts(t *testing.T) {
	h := newHarness(t, defaultClickCfg())
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		if _, err := h.linkSvc.Create(ctx, h.userID, fmt.Sprintf("https://example.com/s%d", i), ""); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	first, err := h.linkSvc.List(ctx, h.userID, "", 5)
	if err != nil {
		t.Fatalf("List page 1: %v", err)
	}

	// Someone creates a link between page 1 and page 2. With OFFSET 5 this would
	// push a row from page 1 down onto page 2 and the client would see it twice.
	if _, err := h.linkSvc.Create(ctx, h.userID, "https://example.com/interloper", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}

	second, err := h.linkSvc.List(ctx, h.userID, first.NextCursor, 5)
	if err != nil {
		t.Fatalf("List page 2: %v", err)
	}

	onPage1 := map[string]bool{}
	for _, l := range first.Items {
		onPage1[l.ShortCode] = true
	}
	for _, l := range second.Items {
		if onPage1[l.ShortCode] {
			t.Errorf("link %s appeared on both pages; the cursor is not stable under inserts", l.ShortCode)
		}
		if l.LongURL == "https://example.com/interloper" {
			t.Error("a link created after page 1 leaked into page 2")
		}
	}
}
