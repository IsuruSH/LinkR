// Package migrations embeds the numbered SQL files and runs them.
//
// They are embedded rather than copied into the image as loose files so the
// distroless container carries no filesystem beyond the binary, and so
// `-migrate` cannot silently run a different schema than the one this binary
// was compiled against.
//
// The same files are sqlc's schema source. That is the point: one definition of
// the schema, type-checked against every query at build time.
package migrations

import (
	"context"
	"embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed *.sql
var FS embed.FS

// Up applies every pending migration.
//
// goose speaks database/sql, so we borrow the pgxpool's config to open a
// short-lived stdlib connection and close it immediately. The application never
// touches database/sql; this is the one exception, and it lasts milliseconds.
//
// Concurrency note: with `--scale backend=N` every replica calls this on boot.
// goose takes an advisory lock around the migration transaction, so exactly one
// replica applies a given version and the rest observe it as already applied.
func Up(ctx context.Context, pool *pgxpool.Pool) error {
	goose.SetBaseFS(FS)
	goose.SetLogger(goose.NopLogger())

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose dialect: %w", err)
	}

	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()

	if err := goose.UpContext(ctx, sqlDB, "."); err != nil {
		return fmt.Errorf("applying migrations: %w", err)
	}
	return nil
}

// Down rolls back the most recent migration. Exposed for `make migrate-down`
// during development; never called by the server.
func Down(ctx context.Context, pool *pgxpool.Pool) error {
	goose.SetBaseFS(FS)
	goose.SetLogger(goose.NopLogger())

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose dialect: %w", err)
	}

	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()

	return goose.DownContext(ctx, sqlDB, ".")
}
