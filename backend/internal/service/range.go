package service

import (
	"time"

	"github.com/IsuruSh/linkr/internal/domain"
)

// Range is a stats time window as the API exposes it.
//
// The spec leaves the stats shape open. We bucket by UTC day: a link's traffic
// is global, so bucketing in the viewer's local time would make the same link
// show different daily totals to different users, and would make the numbers
// non-additive across a timezone boundary. The frontend renders UTC days.
type Range string

const (
	Range7d  Range = "7d"
	Range30d Range = "30d"
	RangeAll Range = "all"
)

// ParseRange defaults to 7d rather than erroring: a bad ?range= value is not
// worth a 400 on a read-only endpoint.
func ParseRange(s string) Range {
	switch Range(s) {
	case Range30d:
		return Range30d
	case RangeAll:
		return RangeAll
	default:
		return Range7d
	}
}

// Window resolves the range against "now" and the link's creation time.
//
// Returns a half-open [Start, End) timestamptz range plus the inclusive day
// bounds that generate_series zero-fills. End is the start of tomorrow, so
// today's clicks are included right up to the current instant without the
// query needing to know what "now" is.
func (r Range) Window(now time.Time, linkCreatedAt time.Time) domain.StatsWindow {
	now = now.UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	var fromDay time.Time
	switch r {
	case Range30d:
		fromDay = today.AddDate(0, 0, -29) // inclusive of today => 30 buckets
	case RangeAll:
		created := linkCreatedAt.UTC()
		fromDay = time.Date(created.Year(), created.Month(), created.Day(), 0, 0, 0, 0, time.UTC)
		// A clock skew or a backdated seed could put creation after today.
		if fromDay.After(today) {
			fromDay = today
		}
	default: // Range7d
		fromDay = today.AddDate(0, 0, -6) // inclusive of today => 7 buckets
	}

	return domain.StatsWindow{
		FromDay: fromDay,
		ToDay:   today,
		Start:   fromDay,
		End:     today.AddDate(0, 0, 1),
	}
}
