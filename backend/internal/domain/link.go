package domain

import (
	"time"

	"github.com/google/uuid"
)

// Link is a short code pointing at a long URL, owned by a user.
type Link struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	ShortCode  string
	LongURL    string
	ClickCount int64
	CreatedAt  time.Time
	// ExpiresAt is nil for links that never expire.
	ExpiresAt *time.Time
}

// IsExpired reports whether the link has passed its expiry as of now. A link
// with no expiry never expires.
func (l Link) IsExpired(now time.Time) bool {
	return l.ExpiresAt != nil && !now.Before(*l.ExpiresAt)
}

// User is an account. PasswordHash never leaves the repository layer in a
// response DTO; there is no JSON tag on this struct for exactly that reason.
type User struct {
	ID           uuid.UUID
	Email        string
	PasswordHash string
	CreatedAt    time.Time
}

// ClickEvent is what the redirect handler hands to the worker. It is a value,
// not a pointer, so it is copied into the channel and the handler's stack frame
// can go away immediately.
type ClickEvent struct {
	LinkID    uuid.UUID
	ClickedAt time.Time
	Referrer  string
	UserAgent string
}

// DailyClicks is one bucket of the stats response.
type DailyClicks struct {
	Day    time.Time
	Clicks int64
}

// StatsWindow is a resolved time window for the stats query.
//
// [Start, End) is the half-open timestamptz range the WHERE clause filters on;
// FromDay..ToDay are the inclusive day bounds generate_series zero-fills. Both
// are precomputed by the service so the SQL predicate stays sargable, and both
// live here because the repository and the service must agree on the shape.
type StatsWindow struct {
	FromDay time.Time
	ToDay   time.Time
	Start   time.Time
	End     time.Time
}

// LinkStats is the stats aggregate: the headline total plus the daily series.
//
// TotalClicks comes from links.click_count, not from summing Series. The series
// is windowed (7d/30d) while the total is lifetime, so summing the window would
// silently under-report.
type LinkStats struct {
	Link        Link
	TotalClicks int64
	Series      []DailyClicks
}

// Page is a keyset-paginated result. NextCursor is empty when the page is last.
type Page[T any] struct {
	Items      []T
	NextCursor string
}
