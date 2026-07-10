// Package seed inserts demo data: one account, a handful of links, and a month
// of backdated clicks so the stats chart has shape on first load.
//
// It lives in internal/ rather than in cmd/server so it is importable — the
// integration tests and any future fixture tooling can call Run directly, which
// nothing could do while it sat in package main.
package seed

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IsuruSh/linkr/internal/auth"
)

// Demo credentials. Printed on seed so a reviewer never has to read this file.
// Obviously not a secret.
const (
	Email    = "demo@linkr.dev"
	Password = "demo-password-123"
)

// advisoryLockKey is arbitrary. Postgres advisory locks are keyed by a bare
// int64; the value only has to be unique within this application.
const advisoryLockKey int64 = 0x1189_5EED

// historyDays is how far back the generated click history reaches.
const historyDays = 30

type link struct {
	code    string
	longURL string
	// weight shapes the generated history. Zero means "create the link but give
	// it no clicks", which exercises the empty-stats path in the UI.
	weight int
}

var demoLinks = []link{
	{"gh-repo", "https://github.com/golang/go", 40},
	{"go-proverbs", "https://go-proverbs.github.io/", 18},
	{"pgx-docs", "https://pkg.go.dev/github.com/jackc/pgx/v5", 9},
	{"sqlc", "https://docs.sqlc.dev/en/latest/", 4},
	{"nextjs", "https://nextjs.org/docs/app", 0},
}

// RunIfEmpty seeds a fresh database, so that `docker compose up --build` alone
// produces a demo you can log into.
//
// Two guards. It must never run in production — the caller enforces that. And it
// takes an advisory lock, because `--scale backend=3` boots three replicas that
// all reach this line at once: without the lock each sees an empty users table
// and they triple the click history. The losers find a non-empty table and skip.
func RunIfEmpty(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquiring connection for seed: %w", err)
	}
	defer conn.Release()

	// Session-scoped, not transaction-scoped: the seed spans many statements and
	// the lock has to be held across all of them.
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockKey); err != nil {
		return fmt.Errorf("taking seed advisory lock: %w", err)
	}
	defer func() {
		if _, err := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, advisoryLockKey); err != nil {
			log.Warn("releasing seed advisory lock", "error", err)
		}
	}()

	var users int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&users); err != nil {
		return fmt.Errorf("counting users: %w", err)
	}
	if users > 0 {
		log.Debug("database already has users, skipping auto-seed")
		return nil
	}

	log.Info("empty development database detected, seeding demo data")
	return Run(ctx, pool, log)
}

// Run inserts the demo data. It is idempotent: running it twice is a no-op
// rather than an error, so `make seed` cannot wedge a demo.
//
// Clicks are written directly rather than through the click worker, because the
// worker stamps ClickedAt with time.Now() and we need history.
func Run(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	hash, err := auth.HashPassword(Password)
	if err != nil {
		return fmt.Errorf("hashing seed password: %w", err)
	}

	var userID uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash) VALUES ($1, $2)
		ON CONFLICT (email) DO UPDATE SET email = EXCLUDED.email
		RETURNING id`, Email, hash).Scan(&userID)
	if err != nil {
		return fmt.Errorf("seeding user: %w", err)
	}

	// A fixed source, so the same seed produces the same chart every time and a
	// screenshot in the README matches what the reviewer sees.
	rng := rand.New(rand.NewSource(42))

	for _, l := range demoLinks {
		if err := seedLink(ctx, pool, log, rng, userID, l); err != nil {
			return err
		}
	}

	log.Info("seed complete", "email", Email, "password", Password)
	fmt.Printf("\n  Demo user ready:\n    email:    %s\n    password: %s\n\n", Email, Password)
	return nil
}

func seedLink(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, rng *rand.Rand, userID uuid.UUID, l link) error {
	var linkID uuid.UUID
	var existed bool

	// `xmax != 0` is how you tell "the ON CONFLICT branch fired" from "inserted".
	err := pool.QueryRow(ctx, `
		INSERT INTO links (user_id, short_code, long_url) VALUES ($1, $2, $3)
		ON CONFLICT (short_code) DO UPDATE SET long_url = EXCLUDED.long_url
		RETURNING id, (xmax != 0) AS existed`,
		userID, l.code, l.longURL).Scan(&linkID, &existed)
	if err != nil {
		return fmt.Errorf("seeding link %q: %w", l.code, err)
	}

	if existed {
		log.Info("seed link already present, skipping clicks", "code", l.code)
		return nil
	}
	if l.weight == 0 {
		return nil
	}

	total, err := seedClicks(ctx, pool, rng, linkID, l.weight)
	if err != nil {
		return fmt.Errorf("seeding clicks for %q: %w", l.code, err)
	}

	// The denormalized counter is normally maintained by the click worker inside
	// the same transaction as the insert. Seeding bypasses the worker, so it must
	// set the counter itself or the dashboard shows zeros.
	if _, err := pool.Exec(ctx, `UPDATE links SET click_count = $1 WHERE id = $2`, total, linkID); err != nil {
		return fmt.Errorf("setting click_count for %q: %w", l.code, err)
	}

	log.Info("seeded link", "code", l.code, "clicks", total)
	return nil
}

func seedClicks(ctx context.Context, pool *pgxpool.Pool, rng *rand.Rand, linkID uuid.UUID, weight int) (int, error) {
	now := time.Now().UTC()
	total := 0

	for dayOffset := historyDays - 1; dayOffset >= 0; dayOffset-- {
		// Recent days get more traffic, so the chart trends instead of looking
		// like noise.
		recency := float64(historyDays-dayOffset) / float64(historyDays)
		n := rng.Intn(weight/2+1) + int(float64(weight)*recency*0.5)

		for i := 0; i < n; i++ {
			clickedAt := now.AddDate(0, 0, -dayOffset).
				Add(-time.Duration(rng.Intn(24)) * time.Hour).
				Add(-time.Duration(rng.Intn(60)) * time.Minute)

			_, err := pool.Exec(ctx,
				`INSERT INTO clicks (link_id, clicked_at, referrer, user_agent) VALUES ($1, $2, $3, $4)`,
				linkID, clickedAt, randomReferrer(rng), randomUserAgent(rng))
			if err != nil {
				return total, err
			}
			total++
		}
	}
	return total, nil
}

// randomReferrer returns nil sometimes, which is direct traffic and exercises
// the NULL path in the clicks table.
func randomReferrer(rng *rand.Rand) any {
	refs := []any{
		nil,
		"https://news.ycombinator.com/",
		"https://x.com/",
		"https://www.google.com/",
	}
	return refs[rng.Intn(len(refs))]
}

func randomUserAgent(rng *rand.Rand) string {
	agents := []string{
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:121.0) Gecko/20100101 Firefox/121.0",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_2 like Mac OS X) AppleWebKit/605.1.15 Version/17.2 Mobile/15E148 Safari/604.1",
	}
	return agents[rng.Intn(len(agents))]
}
