// Command server is the Linkr API. All dependency wiring lives in this file:
// nothing else constructs a pool, a Redis client, or a worker. Reading main.go
// top to bottom should tell you the entire shape of the process.
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
	chimw "github.com/go-chi/chi/v5/middleware"
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
	"github.com/IsuruSh/linkr/internal/service"
	"github.com/IsuruSh/linkr/internal/worker"
	"github.com/IsuruSh/linkr/migrations"
)

func main() {
	// -healthcheck exists because the distroless final image has no shell and no
	// curl, so the container healthcheck has to be the binary itself.
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
	cfg, err := config.Load()
	if err != nil {
		// Config errors happen before the logger exists, and they are the most
		// common first-run failure, so make them impossible to miss.
		return err
	}

	logger := newLogger(cfg)
	slog.SetDefault(logger)

	ctx := context.Background()

	// ---- infrastructure -----------------------------------------------------

	pool, err := newPool(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connecting to postgres: %w", err)
	}
	defer pool.Close()

	// Migrations run on every boot. goose takes an advisory lock, so N replicas
	// starting at once is safe: one applies, the rest no-op.
	migrateCtx, cancelMigrate := context.WithTimeout(ctx, 60*time.Second)
	err = migrations.Up(migrateCtx, pool)
	cancelMigrate()
	if err != nil {
		return err
	}
	logger.Info("migrations applied")

	if migrateOnly {
		return nil
	}
	if seedOnly {
		return seed(ctx, pool, logger)
	}

	redis, err := cache.NewRedis(ctx, cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("connecting to redis: %w", err)
	}
	defer redis.Close()

	// ---- repositories -------------------------------------------------------
	//
	// LinkRepository and UserRepository take db.DBTX, satisfied by the pool. A
	// read/write split later means passing a replica pool here — a constructor
	// change, not a rewrite. ClickRepository needs the pool itself because it
	// owns a transaction boundary.

	linkRepo := repository.NewLinkRepository(pool)
	userRepo := repository.NewUserRepository(pool)
	clickRepo := repository.NewClickRepository(pool)

	// The adapter fills the port. Asserted here, at the wiring point, so the
	// repository package never imports the worker package.
	var _ worker.BatchInserter = clickRepo

	// ---- async click pipeline -----------------------------------------------

	clickWorker := worker.New(worker.Config{
		BufferSize:    cfg.Click.BufferSize,
		Workers:       cfg.Click.Workers,
		BatchSize:     cfg.Click.BatchSize,
		FlushInterval: cfg.Click.FlushInterval,
		FlushTimeout:  cfg.Click.DrainTimeout,
	}, clickRepo, logger)
	clickWorker.Start()

	// ---- services and handlers ----------------------------------------------

	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTTTL)

	linkSvc := service.NewLinkService(linkRepo, clickRepo, redis, clickWorker, logger, cfg.Cache.TTL, cfg.Cache.NegativeTTL)
	authSvc := service.NewAuthService(userRepo, issuer)

	linkHandler := handler.NewLinkHandler(linkSvc, cfg.PublicBaseURL)
	authHandler := handler.NewAuthHandler(authSvc)

	srv := &http.Server{
		Addr:              net.JoinHostPort("", strconv.Itoa(cfg.Port)),
		Handler:           newRouter(cfg, logger, pool, redis, issuer, linkHandler, authHandler),
		ReadHeaderTimeout: 5 * time.Second, // slowloris
		IdleTimeout:       120 * time.Second,
	}

	// ---- run and shut down --------------------------------------------------

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	logger.Info("linkr ready", "env", cfg.AppEnv, "base_url", cfg.PublicBaseURL)

	select {
	case err := <-serveErr:
		return fmt.Errorf("http server: %w", err)
	case sig := <-shutdown:
		logger.Info("shutdown signal received", "signal", sig.String())
	}

	// Shutdown is strictly ordered, and the order is the whole point.
	//
	// 1. srv.Shutdown: stop accepting, let in-flight handlers finish. When this
	//    returns, no handler goroutine exists.
	// 2. clickWorker.Shutdown: closes the event channel. This is safe ONLY
	//    because of (1) — sending on a closed channel panics, and (1) is the
	//    guarantee that nobody is still sending. Workers then drain the buffer,
	//    bounded by CLICK_DRAIN_TIMEOUT.
	// 3. redis.Close, pool.Close via defer, in reverse construction order.
	//
	// Reversing 1 and 2 is the classic version of this bug: it panics under load
	// and looks like a random crash on deploy.
	httpCtx, cancelHTTP := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelHTTP()
	if err := srv.Shutdown(httpCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	logger.Info("http server stopped accepting connections")

	drainCtx, cancelDrain := context.WithTimeout(context.Background(), cfg.Click.DrainTimeout)
	defer cancelDrain()
	if err := clickWorker.Shutdown(drainCtx); err != nil {
		// Not fatal: we lost buffered analytics, not user data. Exiting non-zero
		// here would make a normal deploy look like a crash.
		logger.Error("click worker did not drain cleanly", "error", err)
	}

	logger.Info("shutdown complete")
	return nil
}

func newRouter(
	cfg config.Config,
	logger *slog.Logger,
	pool *pgxpool.Pool,
	redis *cache.RedisCache,
	issuer *auth.Issuer,
	links *handler.LinkHandler,
	authH *handler.AuthHandler,
) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.Logger(logger))
	r.Use(middleware.Recoverer(logger))
	r.Use(chimw.Timeout(cfg.RequestTimeout)) // chi's is fine; no reason to write our own
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   cfg.CORSOrigins,
		AllowedMethods:   []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodOptions},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Request-Id"},
		ExposedHeaders:   []string{"X-Request-Id", "Location"},
		AllowCredentials: false, // the browser talks to the Next BFF, not to us
		MaxAge:           300,
	}))

	// chi's defaults answer 404/405 in plain text. Override them so that every
	// failure the client can observe uses the same JSON envelope.
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, domain.NewError(domain.CodeNotFound, "no such route"))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, domain.NewError(domain.CodeMethodNotAllowed, "method not allowed for this route"))
	})

	// Liveness: "the process is up." Never touches a dependency. If a dependency
	// were probed here, a Postgres blip would make an orchestrator kill every
	// healthy replica — restarting cannot fix someone else's database.
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// Readiness: "can I serve traffic right now." A failing probe pulls the
	// replica out of the load balancer without killing it.
	r.Get("/readyz", readyzHandler(pool, redis, logger))

	r.Route("/api", func(api chi.Router) {
		api.Post("/auth/register", authH.Register)
		api.Post("/auth/login", authH.Login)

		// Auth is applied to this group, not globally: the redirect below is
		// public. Opting routes in is safer than opting them out.
		api.Group(func(protected chi.Router) {
			protected.Use(middleware.RequireAuth(issuer))

			protected.Post("/links", links.Create)
			protected.Get("/links", links.List)
			protected.Get("/links/{code}/stats", links.Stats)
			protected.Delete("/links/{code}", links.Delete)
		})
	})

	// The redirect. Public, registered last, at the root. domain.reservedAliases
	// keeps a custom alias from ever shadowing /api, /healthz, or /readyz.
	r.Get("/{code}", links.Redirect)

	return r
}

// readyzHandler probes both dependencies. Postgres being down is fatal to
// readiness. Redis being down is NOT: the redirect degrades to a Postgres read
// and keeps working, so we report "degraded" and stay in rotation rather than
// taking the whole service offline over a cache outage.
func readyzHandler(pool *pgxpool.Pool, redis *cache.RedisCache, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		checks := map[string]string{"postgres": "ok", "redis": "ok"}
		status := http.StatusOK

		if err := pool.Ping(ctx); err != nil {
			checks["postgres"] = "unreachable"
			status = http.StatusServiceUnavailable
			logger.WarnContext(ctx, "readiness probe failed", "dependency", "postgres", "error", err)
		}
		if err := redis.Ping(ctx); err != nil {
			checks["redis"] = "unreachable"
			logger.WarnContext(ctx, "cache probe failed; redirects will fall back to postgres",
				"dependency", "redis", "error", err)
		}

		state := "ready"
		if status != http.StatusOK {
			state = "unavailable"
		} else if checks["redis"] != "ok" {
			state = "degraded"
		}

		httpx.JSON(w, status, map[string]any{"status": state, "checks": checks})
	}
}

// newPool applies the sizing from config.DBConfig, which carries the rationale.
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

	// NewWithConfig is lazy. Ping so a bad DSN or an unreachable database fails
	// at boot rather than on the first request.
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func newLogger(cfg config.Config) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}

	// JSON in production for the log pipeline; human-readable text in dev.
	if cfg.IsProduction() {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

// runHealthcheck is the container healthcheck. It reads BACKEND_PORT directly
// rather than going through config.Load, because a health probe must not fail
// for the same reason the server would — it should report on the running server.
func runHealthcheck() int {
	port := os.Getenv("BACKEND_PORT")
	if port == "" {
		port = "8080"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/readyz")
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: status %d\n", resp.StatusCode)
		return 1
	}
	return 0
}
