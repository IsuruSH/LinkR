// Package service holds business logic and orchestration. It depends on
// interfaces, never on pgx or Redis directly, which is what makes it testable
// without either.
package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/IsuruSh/linkr/internal/cache"
	"github.com/IsuruSh/linkr/internal/domain"
	"github.com/IsuruSh/linkr/internal/worker"
)

// LinkRepo is the persistence surface the link service needs. Declared here,
// in the consumer, so the repository can grow methods this package never sees.
type LinkRepo interface {
	Create(ctx context.Context, userID uuid.UUID, shortCode, longURL string) (domain.Link, error)
	GetByShortCode(ctx context.Context, code string) (domain.Link, error)
	GetByShortCodeForUser(ctx context.Context, userID uuid.UUID, code string) (domain.Link, error)
	DeleteByShortCode(ctx context.Context, userID uuid.UUID, code string) error
	ListPage(ctx context.Context, userID uuid.UUID, after *domain.Cursor, limit int32) ([]domain.Link, bool, error)
}

// ClickRepo reads the stats aggregation.
type ClickRepo interface {
	ClicksPerDay(ctx context.Context, linkID uuid.UUID, window domain.StatsWindow) ([]domain.DailyClicks, error)
}

type LinkService struct {
	links    LinkRepo
	clicks   ClickRepo
	cache    cache.Cache
	recorder worker.Recorder
	logger   *slog.Logger

	cacheTTL    time.Duration
	negativeTTL time.Duration
}

func NewLinkService(
	links LinkRepo,
	clicks ClickRepo,
	c cache.Cache,
	recorder worker.Recorder,
	logger *slog.Logger,
	cacheTTL, negativeTTL time.Duration,
) *LinkService {
	return &LinkService{
		links:       links,
		clicks:      clicks,
		cache:       c,
		recorder:    recorder,
		logger:      logger,
		cacheTTL:    cacheTTL,
		negativeTTL: negativeTTL,
	}
}

// Create validates the URL, then either honours a custom alias or generates a
// code, retrying on collision.
//
// The two paths differ in how they treat a duplicate: a user-chosen alias that
// is taken is a 409 the user must resolve, while a generated code that collides
// is our problem and we simply try again.
func (s *LinkService) Create(ctx context.Context, userID uuid.UUID, rawURL, alias string) (domain.Link, error) {
	longURL, err := domain.ValidateLongURL(rawURL)
	if err != nil {
		return domain.Link{}, err
	}

	if alias != "" {
		if err := domain.ValidateAlias(alias); err != nil {
			return domain.Link{}, err
		}
		link, err := s.links.Create(ctx, userID, alias, longURL)
		if err != nil {
			return domain.Link{}, err // ErrAliasTaken surfaces as 409
		}
		return link, nil
	}

	for attempt := 0; attempt < domain.MaxCodeGenerationAttempts; attempt++ {
		code, err := domain.GenerateShortCode()
		if err != nil {
			return domain.Link{}, err
		}

		link, err := s.links.Create(ctx, userID, code, longURL)
		if err == nil {
			return link, nil
		}
		if errors.Is(err, domain.ErrAliasTaken) {
			// Lost a race against the unique index. Over a 3.5e12 keyspace this
			// is vanishingly rare, so it is worth a log line when it happens.
			s.logger.WarnContext(ctx, "short code collision, regenerating",
				"code", code, "attempt", attempt+1)
			continue
		}
		return domain.Link{}, err
	}

	return domain.Link{}, domain.ErrCodeGenerationExhausted
}

// Resolve is the redirect hot path: Redis, then Postgres, then cache-fill.
//
// Redis being unreachable degrades to a Postgres read rather than failing the
// request. The redirect is the one thing that must keep working; a slow redirect
// beats a broken one. /readyz reports the cache as unhealthy so the replica is
// pulled from rotation, but in-flight requests still get served.
func (s *LinkService) Resolve(ctx context.Context, code string) (cache.Entry, error) {
	entry, lookup, err := s.cache.GetLink(ctx, code)
	switch {
	case err != nil:
		s.logger.WarnContext(ctx, "cache read failed, falling back to postgres",
			"error", err, "code", code)
	case lookup == cache.Hit:
		return entry, nil
	case lookup == cache.Negative:
		// Postgres said "no such code" recently. Answer without asking again.
		return cache.Entry{}, domain.ErrLinkNotFound
	}

	link, err := s.links.GetByShortCode(ctx, code)
	if err != nil {
		if errors.Is(err, domain.ErrLinkNotFound) {
			// Negative-cache the miss so a scan for /aaaaaaa, /aaaaaab, ...
			// hits Redis instead of hammering Postgres.
			s.cacheMissing(ctx, code)
		}
		return cache.Entry{}, err
	}

	e := cache.Entry{LinkID: link.ID, LongURL: link.LongURL}
	s.cacheFill(ctx, code, e)
	return e, nil
}

// RecordClick hands the event to the worker. It never blocks and never errors:
// the redirect has already been written by the time this is called.
func (s *LinkService) RecordClick(linkID uuid.UUID, referrer, userAgent string) {
	s.recorder.Record(domain.ClickEvent{
		LinkID:    linkID,
		ClickedAt: time.Now().UTC(),
		Referrer:  truncate(referrer, 512),
		UserAgent: truncate(userAgent, 512),
	})
}

// Delete removes a link and invalidates its cache entry.
//
// Order matters: delete from Postgres first, then invalidate. The reverse would
// leave a window where a concurrent Resolve re-populates the cache from a row
// that is about to disappear, resurrecting a deleted link for a full TTL.
//
// Even in this order a narrow window exists: a Resolve that read the row just
// before the delete may write its cache entry just after the invalidate. Closing
// it properly needs a version stamp or a delete marker. The exposure is one
// in-flight request, and the entry expires within CACHE_TTL — noted in
// DECISIONS.md rather than papered over.
func (s *LinkService) Delete(ctx context.Context, userID uuid.UUID, code string) error {
	if err := s.links.DeleteByShortCode(ctx, userID, code); err != nil {
		return err
	}
	if err := s.cache.Invalidate(ctx, code); err != nil {
		// The row is gone; a stale cache entry would serve a dead link for up to
		// CACHE_TTL. That is a correctness problem, so it is an error, not a warn.
		s.logger.ErrorContext(ctx, "link deleted but cache invalidation failed",
			"error", err, "code", code)
		return domain.WrapError(err, domain.CodeInternal, "link deleted but cache may be stale")
	}
	return nil
}

// List returns a keyset page of the user's links.
func (s *LinkService) List(ctx context.Context, userID uuid.UUID, cursorToken string, limit int) (domain.Page[domain.Link], error) {
	var after *domain.Cursor
	if cursorToken != "" {
		c, err := domain.DecodeCursor(cursorToken)
		if err != nil {
			return domain.Page[domain.Link]{}, err
		}
		after = &c
	}

	links, hasMore, err := s.links.ListPage(ctx, userID, after, domain.ClampPageSize(limit))
	if err != nil {
		return domain.Page[domain.Link]{}, err
	}

	page := domain.Page[domain.Link]{Items: links}
	if hasMore && len(links) > 0 {
		last := links[len(links)-1]
		page.NextCursor = domain.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}.Encode()
	}
	return page, nil
}

// Stats returns the headline total plus a zero-filled daily series.
//
// TotalClicks comes from links.click_count (lifetime), not from summing the
// series (windowed). Summing a 7-day window and calling it "total clicks" would
// under-report every link older than a week.
func (s *LinkService) Stats(ctx context.Context, userID uuid.UUID, code string, rng Range) (domain.LinkStats, error) {
	link, err := s.links.GetByShortCodeForUser(ctx, userID, code)
	if err != nil {
		return domain.LinkStats{}, err
	}

	window := rng.Window(time.Now().UTC(), link.CreatedAt)
	series, err := s.clicks.ClicksPerDay(ctx, link.ID, window)
	if err != nil {
		return domain.LinkStats{}, err
	}

	return domain.LinkStats{
		Link:        link,
		TotalClicks: link.ClickCount,
		Series:      series,
	}, nil
}

func (s *LinkService) cacheFill(ctx context.Context, code string, e cache.Entry) {
	if err := s.cache.SetLink(ctx, code, e, s.cacheTTL); err != nil {
		// A failed fill costs a Postgres read next time. Not worth failing on.
		s.logger.WarnContext(ctx, "cache fill failed", "error", err, "code", code)
	}
}

func (s *LinkService) cacheMissing(ctx context.Context, code string) {
	if err := s.cache.SetMissing(ctx, code, s.negativeTTL); err != nil {
		s.logger.WarnContext(ctx, "negative cache write failed", "error", err, "code", code)
	}
}

// truncate bounds a header we do not control. A user agent is attacker-supplied
// and unbounded; storing it verbatim lets anyone write megabytes per click.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
