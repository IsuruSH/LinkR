package main

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

// Demo credentials. Printed on seed so the reviewer can log in without reading
// this file. Obviously not a secret.
const (
	seedEmail    = "demo@linkr.dev"
	seedPassword = "demo-password-123"
)

type seedLink struct {
	code    string
	longURL string
	// clickWeight shapes the generated history so the chart has visible variance
	// instead of a flat line.
	clickWeight int
}

var seedLinks = []seedLink{
	{"gh-repo", "https://github.com/golang/go", 40},
	{"go-proverbs", "https://go-proverbs.github.io/", 18},
	{"pgx-docs", "https://pkg.go.dev/github.com/jackc/pgx/v5", 9},
	{"sqlc", "https://docs.sqlc.dev/en/latest/", 4},
	{"nextjs", "https://nextjs.org/docs/app", 0}, // exercises the empty-stats path
}

// seed inserts a demo user, a handful of links, and 30 days of backdated clicks.
//
// It is idempotent: re-running it is a no-op rather than an error, so
// `make seed` twice does not wedge a demo. Clicks are written directly here
// rather than through the worker, because the worker stamps ClickedAt with
// time.Now() and we need history.
func seed(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) error {
	hash, err := auth.HashPassword(seedPassword)
	if err != nil {
		return fmt.Errorf("hashing seed password: %w", err)
	}

	var userID uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash) VALUES ($1, $2)
		ON CONFLICT (email) DO UPDATE SET email = EXCLUDED.email
		RETURNING id`, seedEmail, hash).Scan(&userID)
	if err != nil {
		return fmt.Errorf("seeding user: %w", err)
	}

	// Deterministic history: the same seed produces the same chart every time,
	// so a screenshot in the README matches what the reviewer sees.
	rng := rand.New(rand.NewSource(42))
	now := time.Now().UTC()

	for _, sl := range seedLinks {
		var linkID uuid.UUID
		var existed bool
		err := pool.QueryRow(ctx, `
			INSERT INTO links (user_id, short_code, long_url) VALUES ($1, $2, $3)
			ON CONFLICT (short_code) DO UPDATE SET long_url = EXCLUDED.long_url
			RETURNING id, (xmax != 0) AS existed`,
			userID, sl.code, sl.longURL).Scan(&linkID, &existed)
		if err != nil {
			return fmt.Errorf("seeding link %q: %w", sl.code, err)
		}
		if existed {
			logger.Info("seed link already present, skipping clicks", "code", sl.code)
			continue
		}
		if sl.clickWeight == 0 {
			continue
		}

		total := 0
		for dayOffset := 29; dayOffset >= 0; dayOffset-- {
			// Recent days get more traffic, so the chart trends rather than
			// looking like noise.
			recency := float64(30-dayOffset) / 30.0
			n := rng.Intn(sl.clickWeight/2+1) + int(float64(sl.clickWeight)*recency*0.5)

			for i := 0; i < n; i++ {
				clickedAt := now.AddDate(0, 0, -dayOffset).
					Add(-time.Duration(rng.Intn(24)) * time.Hour).
					Add(-time.Duration(rng.Intn(60)) * time.Minute)

				if _, err := pool.Exec(ctx,
					`INSERT INTO clicks (link_id, clicked_at, referrer, user_agent) VALUES ($1, $2, $3, $4)`,
					linkID, clickedAt, seedReferrer(rng), seedUserAgent(rng)); err != nil {
					return fmt.Errorf("seeding clicks for %q: %w", sl.code, err)
				}
				total++
			}
		}

		// The denormalized counter is normally maintained by the worker inside
		// the same transaction as the insert. Seeding bypasses the worker, so it
		// must set the counter itself or the dashboard would show zeros.
		if _, err := pool.Exec(ctx,
			`UPDATE links SET click_count = $1 WHERE id = $2`, total, linkID); err != nil {
			return fmt.Errorf("setting click_count for %q: %w", sl.code, err)
		}
		logger.Info("seeded link", "code", sl.code, "clicks", total)
	}

	logger.Info("seed complete", "email", seedEmail, "password", seedPassword)
	fmt.Printf("\n  Demo user ready:\n    email:    %s\n    password: %s\n\n", seedEmail, seedPassword)
	return nil
}

func seedReferrer(rng *rand.Rand) any {
	refs := []any{
		nil, // direct traffic; exercises the NULL path
		"https://news.ycombinator.com/",
		"https://x.com/",
		"https://www.google.com/",
	}
	return refs[rng.Intn(len(refs))]
}

func seedUserAgent(rng *rand.Rand) string {
	agents := []string{
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:121.0) Gecko/20100101 Firefox/121.0",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_2 like Mac OS X) AppleWebKit/605.1.15 Version/17.2 Mobile/15E148 Safari/604.1",
	}
	return agents[rng.Intn(len(agents))]
}
