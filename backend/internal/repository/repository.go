// Package repository adapts sqlc's generated types to domain entities and
// Postgres error codes to domain errors.
//
// It is deliberately thin. Everything above it sees domain.Link and
// domain.ErrAliasTaken; nothing above it imports pgx, pgconn, or internal/db.
// That is what lets the service layer be tested without a database, and what
// keeps a driver upgrade from rippling through the codebase.
package repository

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/IsuruSh/linkr/internal/domain"
)

// Postgres error codes we care about. https://www.postgresql.org/docs/16/errcodes-appendix.html
const (
	pgUniqueViolation      = "23505"
	pgCheckViolation       = "23514"
	pgForeignKeyViolation  = "23503"
	pgDeadlockDetected     = "40P01"
	pgSerializationFailure = "40001"
)

// Constraint names from migrations/00001_init.sql. A unique violation is only
// meaningful once you know which index rejected it: the same 23505 means
// "alias taken" on links and "email taken" on users.
const (
	constraintShortCodeUnique = "links_short_code_key"
	constraintEmailUnique     = "users_email_key"
	constraintShortCodeFormat = "links_short_code_format"
)

// isUniqueViolation reports whether err is a 23505 on the named constraint.
func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == pgUniqueViolation && pgErr.ConstraintName == constraint
}

func isCheckViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == pgCheckViolation && pgErr.ConstraintName == constraint
}

// IsRetryable reports whether a failed transaction is worth retrying. Deadlocks
// and serialization failures are transient by definition: the loser of a
// deadlock did nothing wrong. Used by the click worker's batch insert.
func IsRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == pgDeadlockDetected || pgErr.Code == pgSerializationFailure
}

// notFound maps pgx.ErrNoRows onto a domain error. Everything else passes
// through unchanged so httpx renders it as an opaque 500.
func notFound(err error, sentinel *domain.Error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return sentinel
	}
	return err
}
