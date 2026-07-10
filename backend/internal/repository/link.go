package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/IsuruSh/linkr/internal/db"
	"github.com/IsuruSh/linkr/internal/domain"
)

// LinkRepository reads and writes links.
//
// It takes db.DBTX, not *pgxpool.Pool. That interface is satisfied by the pool,
// by a transaction, and — the point — by a read-replica pool. Introducing a
// read/write split later is a constructor change in main.go, not a rewrite here.
type LinkRepository struct {
	q *db.Queries
}

func NewLinkRepository(dbtx db.DBTX) *LinkRepository {
	return &LinkRepository{q: db.New(dbtx)}
}

func toDomainLink(l db.Link) domain.Link {
	return domain.Link{
		ID:         l.ID,
		UserID:     l.UserID,
		ShortCode:  l.ShortCode,
		LongURL:    l.LongUrl,
		ClickCount: l.ClickCount,
		CreatedAt:  l.CreatedAt,
	}
}

// Create inserts a link. A duplicate short code surfaces as domain.ErrAliasTaken,
// whether the code was user-chosen or generated — the service decides whether to
// retry (generated) or report a conflict (chosen).
func (r *LinkRepository) Create(ctx context.Context, userID uuid.UUID, shortCode, longURL string) (domain.Link, error) {
	row, err := r.q.CreateLink(ctx, db.CreateLinkParams{
		UserID:    userID,
		ShortCode: shortCode,
		LongUrl:   longURL,
	})
	switch {
	case isUniqueViolation(err, constraintShortCodeUnique):
		return domain.Link{}, domain.ErrAliasTaken
	case isCheckViolation(err, constraintShortCodeFormat):
		// The application validates aliases before it gets here, so reaching this
		// means validation and the CHECK constraint have drifted apart.
		return domain.Link{}, domain.NewError(domain.CodeInvalidAlias, "short code rejected by the database constraint")
	case err != nil:
		return domain.Link{}, fmt.Errorf("creating link: %w", err)
	}
	return toDomainLink(row), nil
}

// GetByShortCode is the redirect path's database read, taken only on cache miss.
func (r *LinkRepository) GetByShortCode(ctx context.Context, code string) (domain.Link, error) {
	row, err := r.q.GetLinkByShortCode(ctx, code)
	if err != nil {
		return domain.Link{}, notFound(err, domain.ErrLinkNotFound)
	}
	return toDomainLink(row), nil
}

// GetByShortCodeForUser scopes the lookup to an owner. A link owned by someone
// else is reported as not-found, not as forbidden: 403 would confirm the code
// exists, turning the stats endpoint into an oracle for enumerating other
// users' short codes.
func (r *LinkRepository) GetByShortCodeForUser(ctx context.Context, userID uuid.UUID, code string) (domain.Link, error) {
	row, err := r.q.GetLinkByShortCodeForUser(ctx, db.GetLinkByShortCodeForUserParams{
		ShortCode: code,
		UserID:    userID,
	})
	if err != nil {
		return domain.Link{}, notFound(err, domain.ErrLinkNotFound)
	}
	return toDomainLink(row), nil
}

// DeleteByShortCode removes a link the user owns. Clicks cascade.
func (r *LinkRepository) DeleteByShortCode(ctx context.Context, userID uuid.UUID, code string) error {
	_, err := r.q.DeleteLinkByShortCode(ctx, db.DeleteLinkByShortCodeParams{
		ShortCode: code,
		UserID:    userID,
	})
	if err != nil {
		return notFound(err, domain.ErrLinkNotFound)
	}
	return nil
}

// ListPage returns up to limit links for a user, newest first, plus whether a
// further page exists.
//
// It asks the database for limit+1 rows. If the extra row comes back there is
// another page; it is trimmed and never returned. This avoids a second COUNT(*)
// query purely to answer "is there more".
//
// after == nil means the first page. The two cases are separate queries because
// sqlc emits static SQL, and folding them into one with `OR $2 IS NULL` produces
// a predicate Postgres cannot satisfy from links_user_created_idx.
func (r *LinkRepository) ListPage(ctx context.Context, userID uuid.UUID, after *domain.Cursor, limit int32) ([]domain.Link, bool, error) {
	var rows []db.Link
	var err error

	if after == nil {
		rows, err = r.q.ListLinksFirstPage(ctx, db.ListLinksFirstPageParams{
			UserID:   userID,
			PageSize: limit + 1,
		})
	} else {
		rows, err = r.q.ListLinksAfter(ctx, db.ListLinksAfterParams{
			UserID:         userID,
			AfterCreatedAt: after.CreatedAt,
			AfterID:        after.ID,
			PageSize:       limit + 1,
		})
	}
	if err != nil {
		return nil, false, fmt.Errorf("listing links: %w", err)
	}

	hasMore := len(rows) > int(limit)
	if hasMore {
		rows = rows[:limit]
	}

	links := make([]domain.Link, len(rows))
	for i, row := range rows {
		links[i] = toDomainLink(row)
	}
	return links, hasMore, nil
}
