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

	"github.com/IsuruSh/linkr/internal/config"
	"github.com/IsuruSh/linkr/internal/domain"
	"github.com/IsuruSh/linkr/internal/httpx"
	"github.com/IsuruSh/linkr/internal/middleware"
)

func main() {
	// -healthcheck exists because the distroless final image has no shell and no
	// curl, so the container healthcheck has to be the binary itself.
	healthcheck := flag.Bool("healthcheck", false, "probe the local /readyz endpoint and exit")
	flag.Parse()

	if *healthcheck {
		os.Exit(runHealthcheck())
	}

	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		// Config errors happen before the logger exists, and they are the most
		// common first-run failure, so make them impossible to miss.
		return err
	}

	logger := newLogger(cfg)
	slog.SetDefault(logger)
	logger.Info("starting linkr", "env", cfg.AppEnv, "port", cfg.Port)

	srv := &http.Server{
		Addr:              net.JoinHostPort("", strconv.Itoa(cfg.Port)),
		Handler:           newRouter(cfg, logger),
		ReadHeaderTimeout: 5 * time.Second, // slowloris
		IdleTimeout:       120 * time.Second,
	}

	// Shutdown is strictly ordered, and the order is the whole point:
	//
	//  1. srv.Shutdown  — stop accepting, let in-flight handlers finish.
	//  2. close(clicks) — safe ONLY because (1) guarantees no handler can still
	//                     be sending on the channel. Sending on a closed channel
	//                     panics; this ordering is what makes it impossible.
	//  3. drain workers — bounded by CLICK_DRAIN_TIMEOUT.
	//  4. close redis, close pool.
	//
	// Steps 2-4 arrive with the worker in the next slice.
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

	select {
	case err := <-serveErr:
		return fmt.Errorf("http server: %w", err)
	case sig := <-shutdown:
		logger.Info("shutdown signal received", "signal", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	logger.Info("shutdown complete")
	return nil
}

func newRouter(cfg config.Config, logger *slog.Logger) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.Logger(logger))
	r.Use(middleware.Recoverer(logger))
	r.Use(chimw.Timeout(cfg.RequestTimeout)) // chi's is fine; no reason to write our own
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   cfg.CORSOrigins,
		AllowedMethods:   []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodOptions},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Request-Id"},
		ExposedHeaders:   []string{"X-Request-Id"},
		AllowCredentials: false, // the browser talks to the Next BFF, not to us
		MaxAge:           300,
	}))

	// chi's defaults answer 404/405 in plain text. Override them so that every
	// failure the client can observe uses the same JSON envelope — an unknown
	// route is not a special case.
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, domain.NewError(domain.CodeNotFound, "no such route"))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, domain.NewError(domain.CodeMethodNotAllowed, "method not allowed for this route"))
	})

	// Liveness: "the process is up." Never touches a dependency — if this fails,
	// restarting helps. Readiness answers "can I serve traffic," which is a
	// different question, and it gains real dependency probes in a later slice.
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})

	return r
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
