// Command server is the Linkr API.
//
// Every dependency is constructed here and nowhere else. Reading main.go top to
// bottom should tell you the whole shape of the process: what it connects to,
// what it serves, and how it stops.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IsuruSh/linkr/internal/auth"
	"github.com/IsuruSh/linkr/internal/cache"
	"github.com/IsuruSh/linkr/internal/config"
	"github.com/IsuruSh/linkr/internal/domain"
	"github.com/IsuruSh/linkr/internal/handler"
	"github.com/IsuruSh/linkr/internal/httpx"
	"github.com/IsuruSh/linkr/internal/middleware"
	"github.com/IsuruSh/linkr/internal/repository"
	"github.com/IsuruSh/linkr/internal/seed"
	"github.com/IsuruSh/linkr/internal/service"
	"github.com/IsuruSh/linkr/internal/worker"
	"github.com/IsuruSh/linkr/migrations"
)

const (
	shutdownTimeout  = 15 * time.Second
	migrationTimeout = 60 * time.Second
)

func main() {
	// -healthcheck is how the distroless container probes itself; see healthcheck.go.
	healthcheck := flag.Bool("healthcheck", false, "probe the local /readyz endpoint and exit")
	migrateOnly := flag.Bool("migrate", false, "apply pending migrations and exit")
	seedOnly := flag.Bool("seed", false, "insert demo data and exit")
	flag.Parse()

	if *healthcheck {
		os.Exit(runHealthcheck())
	}

	if err := run(*migrateOnly, *seedOnly); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(migrateOnly, seedOnly bool) error {
	ctx := context.Background()

	// ── Config ────────────────────────────────────────────────────────────
	// Fails fast, and reports every bad variable at once. See internal/config.
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// ── Logger ────────────────────────────────────────────────────────────
	log := buildLogger(cfg)
	slog.SetDefault(log)

	// ── Postgres ──────────────────────────────────────────────────────────
	pool, err := newPool(ctx, cfg)
	if err != nil {
		return fmt.Errorf("postgres init failed: %w", err)
	}
	defer pool.Close()
	log.Info("PostgreSQL connected")

	// ── Migrations ────────────────────────────────────────────────────────
	// Run on every boot. goose takes an advisory lock, so `--scale backend=N`
	// means one replica applies and the rest no-op.
	migrateCtx, cancelMigrate := context.WithTimeout(ctx, migrationTimeout)
	err = migrations.Up(migrateCtx, pool)
	cancelMigrate()
	if err != nil {
		return err
	}
	log.Info("migrations applied")

	if migrateOnly {
		return nil
	}
	if seedOnly {
		return seed.Run(ctx, pool, log)
	}

	// ── Demo data ─────────────────────────────────────────────────────────
	// Only in dev, only when the database is empty. See internal/seed.
	if !cfg.IsProduction() {
		if err := seed.RunIfEmpty(ctx, pool, log); err != nil {
			log.Error("auto-seed failed, continuing without demo data", "error", err)
		}
	}

	// ── Redis ─────────────────────────────────────────────────────────────
	redis, err := cache.NewRedis(ctx, cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("redis init failed: %w", err)
	}
	defer redis.Close()
	log.Info("Redis connected")

	// ── Auth ──────────────────────────────────────────────────────────────
	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTTTL)
	requireAuth := middleware.RequireAuth(issuer)

	// ── Repositories ──────────────────────────────────────────────────────
	// Link and User take db.DBTX, satisfied by the pool today and by a read
	// replica later. Click needs the pool itself: it owns a transaction.
	linkRepo := repository.NewLinkRepository(pool)
	userRepo := repository.NewUserRepository(pool)
	clickRepo := repository.NewClickRepository(pool)

	// The adapter fills the port. Asserted here so repository/ never has to
	// import worker/ and point the dependency arrow backwards.
	var _ worker.BatchInserter = clickRepo

	// ── Click worker ──────────────────────────────────────────────────────
	// Bounded channel, worker pool, batched inserts. See internal/worker.
	clickWorker := worker.New(worker.Config{
		BufferSize:    cfg.Click.BufferSize,
		Workers:       cfg.Click.Workers,
		BatchSize:     cfg.Click.BatchSize,
		FlushInterval: cfg.Click.FlushInterval,
		FlushTimeout:  cfg.Click.DrainTimeout,
	}, clickRepo, log)
	clickWorker.Start()

	// ── Services ──────────────────────────────────────────────────────────
	linkSvc := service.NewLinkService(linkRepo, clickRepo, redis, clickWorker, log, cfg.Cache.TTL, cfg.Cache.NegativeTTL)
	authSvc := service.NewAuthService(userRepo, issuer)

	// ── Handlers ──────────────────────────────────────────────────────────
	linkHandler := handler.NewLinkHandler(linkSvc, cfg.PublicBaseURL)
	authHandler := handler.NewAuthHandler(authSvc)
	healthHandler := handler.NewHealthHandler(pool, redis, log)

	// ── HTTP server ───────────────────────────────────────────────────────
	srv := &http.Server{
		Addr:              net.JoinHostPort("", strconv.Itoa(cfg.Port)),
		Handler:           newRouter(cfg, log, requireAuth, linkHandler, authHandler, healthHandler),
		ReadHeaderTimeout: 5 * time.Second, // slowloris
		IdleTimeout:       120 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	serveErr := make(chan error, 1)
	go func() {
		log.Info("linkr API starting", "addr", srv.Addr, "env", cfg.AppEnv, "base_url", cfg.PublicBaseURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("server error: %w", err)
	case sig := <-stop:
		log.Info("shutting down", "signal", sig.String())
	}

	return shutdown(srv, clickWorker, cfg, log)
}

// newRouter assembles the middleware chain and mounts each handler's route group.
//
// Chain order is deliberate: RequestID first so every later log line and panic
// report carries one; Logger before Recoverer so a panicking request still emits
// exactly one access log, with its 500.
func newRouter(
	cfg config.Config,
	log *slog.Logger,
	requireAuth func(http.Handler) http.Handler,
	links *handler.LinkHandler,
	authH *handler.AuthHandler,
	health *handler.HealthHandler,
) http.Handler {
	// ── CORS ──────────────────────────────────────────────────────────────
	corsHandler := cors.Handler(cors.Options{
		AllowedOrigins:   cfg.CORSOrigins,
		AllowedMethods:   []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodOptions},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Request-Id"},
		ExposedHeaders:   []string{"X-Request-Id", "Location"},
		AllowCredentials: false, // the browser talks to the Next BFF, not to us
		MaxAge:           300,
	})

	// ── Router ────────────────────────────────────────────────────────────
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger(log))
	r.Use(middleware.Recoverer(log))
	r.Use(chiMiddleware.Timeout(cfg.RequestTimeout))
	r.Use(corsHandler)

	// chi answers 404/405 in plain text. Override so every observable failure
	// shares one envelope shape.
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, domain.NewError(domain.CodeNotFound, "no such route"))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, domain.NewError(domain.CodeMethodNotAllowed, "method not allowed for this route"))
	})

	// ── API routes ────────────────────────────────────────────────────────
	// Each handler owns its own group. requireAuth is passed at the mount site,
	// so which groups are guarded is visible in these two lines.
	r.Mount("/api/auth", authH.Routes())
	r.Mount("/api/links", links.Routes(requireAuth))

	// ── Health ────────────────────────────────────────────────────────────
	r.Get("/healthz", health.Live)
	r.Get("/readyz", health.Ready)

	// ── Redirect (public, hot path) ───────────────────────────────────────
	// Registered last, at the root. domain's reserved-alias list keeps a custom
	// alias from ever shadowing /api, /healthz or /readyz.
	r.Get("/{code}", links.Redirect)

	return r
}

// shutdown stops the process in an order that is not negotiable:
//
//  1. srv.Shutdown   stop accepting; in-flight handlers finish.
//  2. clickWorker    closes the event channel. Safe ONLY because (1) returned,
//     which is the guarantee that no handler is still sending.
//     Sending on a closed channel panics.
//  3. redis, pool    closed by the deferred calls in run().
//
// Swapping 1 and 2 panics under load and looks like a random crash on deploy.
func shutdown(srv *http.Server, clickWorker *worker.ClickWorker, cfg config.Config, log *slog.Logger) error {
	httpCtx, cancelHTTP := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelHTTP()

	if err := srv.Shutdown(httpCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("stopped accepting connections")

	drainCtx, cancelDrain := context.WithTimeout(context.Background(), cfg.Click.DrainTimeout)
	defer cancelDrain()

	if err := clickWorker.Shutdown(drainCtx); err != nil {
		// We lost buffered analytics, not user data. Exiting non-zero here would
		// make a routine deploy look like a crash.
		log.Error("click worker did not drain cleanly", "error", err)
	}

	log.Info("server stopped")
	return nil
}

// newPool applies the sizing from config.DBConfig, which carries the rationale
// for each number.
func newPool(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing DATABASE_URL: %w", err)
	}

	poolCfg.MaxConns = cfg.DB.MaxConns
	poolCfg.MinConns = cfg.DB.MinConns
	poolCfg.MaxConnLifetime = cfg.DB.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.DB.MaxConnIdleTime

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, err
	}

	// NewWithConfig is lazy. Ping so a bad DSN fails at boot, not on the first
	// request.
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// buildLogger writes JSON in production for the log pipeline, and human-readable
// text in development.
func buildLogger(cfg config.Config) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}

	if cfg.IsProduction() {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}
