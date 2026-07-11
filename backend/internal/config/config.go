// Package config loads all runtime configuration from the environment into a
// single struct, validating it once at boot. Nothing else in the program reads
// os.Getenv, so there is exactly one place where a missing or malformed setting
// can take the process down — and it does so before we accept a single request.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const minJWTSecretLen = 32

type Config struct {
	AppEnv         string
	LogLevel       string
	Port           int
	PublicBaseURL  string
	AppURL         string
	RequestTimeout time.Duration
	CORSOrigins    []string

	DatabaseURL string
	DB          DBConfig

	RedisURL string
	Cache    CacheConfig

	JWTSecret string
	JWTTTL    time.Duration

	// TrustProxyHeaders makes rate limiting read the client IP from the leftmost
	// X-Forwarded-For entry. Enable it ONLY behind a proxy that sets that header
	// itself and strips inbound copies (e.g. the Nginx reverse proxy in the
	// production stack) — otherwise a client hitting the server directly can spoof
	// XFF and evade or misattribute limits. Default false, so a direct deployment
	// is safe; the production compose forces it true.
	TrustProxyHeaders bool

	// SeedDemoData forces the demo seed to run even outside development.
	//
	// It exists for hosted demos. The final image is distroless — no shell — so
	// there is nothing to exec into to run `/server -seed` by hand. This makes
	// seeding a deploy-time decision instead.
	//
	// It is still safe: the seed no-ops when the users table is non-empty, and it
	// takes an advisory lock so concurrent replicas cannot double-seed.
	SeedDemoData bool

	Click     ClickConfig
	RateLimit RateLimitConfig
}

// DBConfig sizes the pgx pool. The numbers are deliberate, not defaults.
//
// Postgres ships with max_connections=100. At MaxConns=20 per backend replica we
// can run `docker compose up --scale backend=4` and still leave 20 connections
// for migrations, psql, and a monitoring agent. Our queries are short and
// I/O-bound; beyond roughly 2-3x the database's core count, additional
// connections do not add throughput, they just queue inside Postgres and add
// context-switching. MinConns keeps a warm floor so a traffic spike does not pay
// TCP + TLS + auth on the redirect hot path. MaxConnLifetime is what lets
// connections migrate to a new primary after a failover instead of pinning to a
// dead one; MaxConnIdleTime returns capacity during quiet periods.
type DBConfig struct {
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

type CacheConfig struct {
	TTL         time.Duration
	NegativeTTL time.Duration
}

type ClickConfig struct {
	BufferSize    int
	Workers       int
	BatchSize     int
	FlushInterval time.Duration
	DrainTimeout  time.Duration
}

// RateLimitConfig bounds link creation per user. When Enabled is false the
// middleware is not mounted at all, so the disabled path costs nothing per
// request. The window is fixed at one minute; see internal/middleware/ratelimit.go
// for why fixed-window and not a token bucket.
type RateLimitConfig struct {
	Enabled   bool
	PerMinute int
	// AuthPerMinute limits the unauthenticated auth endpoints per client IP.
	// Lower than PerMinute: login and register are the brute-force surface.
	AuthPerMinute int
}

func (c Config) IsProduction() bool { return c.AppEnv == "production" }

// Load reads the environment and fails fast. Every error is collected so a fresh
// deploy reports all of its missing variables at once rather than one per restart.
func Load() (Config, error) {
	var errs []error
	fail := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	cfg := Config{
		AppEnv:            envStr("APP_ENV", "development"),
		LogLevel:          envStr("LOG_LEVEL", "info"),
		PublicBaseURL:     strings.TrimRight(envStr("PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
		AppURL:            strings.TrimRight(envStr("APP_URL", "http://localhost:3000"), "/"),
		DatabaseURL:       envStr("DATABASE_URL", ""),
		RedisURL:          envStr("REDIS_URL", ""),
		JWTSecret:         envStr("JWT_SECRET", ""),
		CORSOrigins:       envCSV("CORS_ALLOWED_ORIGINS", []string{"http://localhost:3000"}),
		SeedDemoData:      envStr("SEED_DEMO_DATA", "") == "true",
		TrustProxyHeaders: envStr("TRUST_PROXY_HEADERS", "") == "true",
	}

	cfg.Port = envInt("BACKEND_PORT", 8080, &errs)
	cfg.RequestTimeout = envDur("REQUEST_TIMEOUT", 10*time.Second, &errs)
	cfg.JWTTTL = envDur("JWT_TTL", 24*time.Hour, &errs)

	cfg.DB = DBConfig{
		MaxConns:        int32(envInt("DB_MAX_CONNS", 20, &errs)),
		MinConns:        int32(envInt("DB_MIN_CONNS", 5, &errs)),
		MaxConnLifetime: envDur("DB_MAX_CONN_LIFETIME", 30*time.Minute, &errs),
		MaxConnIdleTime: envDur("DB_MAX_CONN_IDLE_TIME", 5*time.Minute, &errs),
	}

	cfg.Cache = CacheConfig{
		TTL:         envDur("CACHE_TTL", 24*time.Hour, &errs),
		NegativeTTL: envDur("CACHE_NEGATIVE_TTL", 60*time.Second, &errs),
	}

	cfg.Click = ClickConfig{
		BufferSize:    envInt("CLICK_BUFFER_SIZE", 10_000, &errs),
		Workers:       envInt("CLICK_WORKERS", 4, &errs),
		BatchSize:     envInt("CLICK_BATCH_SIZE", 100, &errs),
		FlushInterval: envDur("CLICK_FLUSH_INTERVAL", 500*time.Millisecond, &errs),
		DrainTimeout:  envDur("CLICK_DRAIN_TIMEOUT", 5*time.Second, &errs),
	}

	cfg.RateLimit = RateLimitConfig{
		Enabled:       envStr("RATE_LIMIT_ENABLED", "true") == "true",
		PerMinute:     envInt("RATE_LIMIT_PER_MINUTE", 30, &errs),
		AuthPerMinute: envInt("RATE_LIMIT_AUTH_PER_MINUTE", 10, &errs),
	}

	if cfg.DatabaseURL == "" {
		fail(errors.New("DATABASE_URL is required"))
	}
	if cfg.RedisURL == "" {
		fail(errors.New("REDIS_URL is required"))
	}
	// A short secret is worse than no secret: it looks configured but is brute-forceable.
	if len(cfg.JWTSecret) < minJWTSecretLen {
		fail(fmt.Errorf("JWT_SECRET must be at least %d bytes, got %d", minJWTSecretLen, len(cfg.JWTSecret)))
	}
	if cfg.IsProduction() && cfg.JWTSecret == "change_me_to_a_long_random_string_at_least_32_bytes" {
		fail(errors.New("JWT_SECRET is still the example value; refusing to start in production"))
	}
	if cfg.DB.MinConns > cfg.DB.MaxConns {
		fail(fmt.Errorf("DB_MIN_CONNS (%d) exceeds DB_MAX_CONNS (%d)", cfg.DB.MinConns, cfg.DB.MaxConns))
	}
	if cfg.Click.BufferSize < 1 || cfg.Click.Workers < 1 || cfg.Click.BatchSize < 1 {
		fail(errors.New("CLICK_BUFFER_SIZE, CLICK_WORKERS and CLICK_BATCH_SIZE must all be >= 1"))
	}
	if cfg.RateLimit.Enabled && (cfg.RateLimit.PerMinute < 1 || cfg.RateLimit.AuthPerMinute < 1) {
		fail(errors.New("RATE_LIMIT_PER_MINUTE and RATE_LIMIT_AUTH_PER_MINUTE must be >= 1 when rate limiting is enabled"))
	}

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("invalid configuration: %w", errors.Join(errs...))
	}
	return cfg, nil
}

func envStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envCSV(key string, def []string) []string {
	raw := envStr(key, "")
	if raw == "" {
		return def
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envInt(key string, def int, errs *[]error) int {
	raw := envStr(key, "")
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %q is not an integer", key, raw))
		return def
	}
	return v
}

func envDur(key string, def time.Duration, errs *[]error) time.Duration {
	raw := envStr(key, "")
	if raw == "" {
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %q is not a duration (e.g. 500ms, 24h)", key, raw))
		return def
	}
	return v
}
