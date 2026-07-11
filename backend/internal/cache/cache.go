// Package cache fronts the redirect hot path.
//
// The interface exists for one concrete reason: the redirect service is tested
// without a Redis, and swapping Redis for an in-process LRU (single-node) or a
// different store is a constructor change in main.go. It is not an empty
// abstraction — there is exactly one production implementation and one fake.
package cache

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Entry is what the redirect needs to serve a request and record its click:
// the target URL, the link ID the ClickEvent is keyed on, and the expiry.
//
// Caching only the URL would be a subtle mistake: every cache hit would still
// need a Postgres round trip to resolve the ID before it could record the
// click, which defeats the point of the cache. The same reasoning applies to
// ExpiresAt — a cache hit must be able to enforce expiry without reading
// Postgres, or an expired link keeps redirecting from cache.
type Entry struct {
	LinkID  uuid.UUID `json:"id"`
	LongURL string    `json:"url"`
	// ExpiresAt is nil for links that never expire. Present so a cache hit can
	// serve 410 without touching the database.
	ExpiresAt *time.Time `json:"exp,omitempty"`
}

// Lookup distinguishes "not cached" from "cached as absent". Without the third
// state, a flood of requests for codes that do not exist would miss the cache
// every time and fall through to Postgres — cheap enumeration of the DB.
type Lookup int

const (
	// Miss: we know nothing; ask Postgres.
	Miss Lookup = iota
	// Hit: entry is valid.
	Hit
	// Negative: Postgres was asked recently and had no such code. Answer 404
	// without touching the database.
	Negative
)

type Cache interface {
	// GetLink never returns an error for a cache miss; a miss is a Lookup value.
	// An error means the cache itself is unreachable, and the caller is expected
	// to degrade to the database rather than fail the request.
	GetLink(ctx context.Context, code string) (Entry, Lookup, error)

	// SetLink caches a resolved link for ttl.
	SetLink(ctx context.Context, code string, e Entry, ttl time.Duration) error

	// SetMissing caches the absence of a code, for a much shorter ttl than a
	// hit: a code that does not exist now may be created a second from now.
	SetMissing(ctx context.Context, code string, ttl time.Duration) error

	// Invalidate removes a code. Called on update and delete. It must succeed
	// or be surfaced: a stale cache entry serves a deleted link, which is a
	// correctness bug, not a performance one.
	Invalidate(ctx context.Context, code string) error

	// Ping backs the /readyz probe.
	Ping(ctx context.Context) error

	Close() error
}
