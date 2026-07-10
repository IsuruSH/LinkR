package service

import (
	"testing"
	"time"
)

func TestParseRange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want Range
	}{
		{"7d", Range7d},
		{"30d", Range30d},
		{"all", RangeAll},
		{"", Range7d},        // default
		{"garbage", Range7d}, // a bad value is not worth a 400 on a read
		{"7D", Range7d},      // case-sensitive; falls back to the default
		{"1y", Range7d},
	}
	for _, tt := range tests {
		if got := ParseRange(tt.in); got != tt.want {
			t.Errorf("ParseRange(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// The bucket count is what the chart draws. Off by one here means "last 7 days"
// renders 8 bars.
func TestRange_WindowBucketCounts(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 10, 14, 30, 0, 0, time.UTC)
	created := now.AddDate(0, 0, -100)

	tests := []struct {
		rng      Range
		wantDays int
	}{
		{Range7d, 7},
		{Range30d, 30},
		{RangeAll, 101}, // 100 days ago through today, inclusive
	}

	for _, tt := range tests {
		t.Run(string(tt.rng), func(t *testing.T) {
			w := tt.rng.Window(now, created)

			days := int(w.ToDay.Sub(w.FromDay).Hours()/24) + 1 // inclusive
			if days != tt.wantDays {
				t.Errorf("window spans %d days, want %d", days, tt.wantDays)
			}
			if !w.ToDay.Equal(time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)) {
				t.Errorf("ToDay = %v, want today at midnight UTC", w.ToDay)
			}
		})
	}
}

// End is exclusive and must be the start of tomorrow, so a click one second ago
// is counted without the SQL needing to know what "now" is.
func TestRange_WindowEndIsStartOfTomorrow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 10, 23, 59, 59, 0, time.UTC)
	w := Range7d.Window(now, now.AddDate(0, 0, -30))

	wantEnd := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	if !w.End.Equal(wantEnd) {
		t.Errorf("End = %v, want %v", w.End, wantEnd)
	}
	if !w.End.After(now) {
		t.Error("a click happening right now would fall outside the window")
	}
	if !w.Start.Equal(w.FromDay) {
		t.Errorf("Start (%v) must equal FromDay (%v)", w.Start, w.FromDay)
	}
}

// "all" starts at the link's creation day, not at the epoch: generate_series
// from 1970 would produce ~20,000 zero rows per request.
func TestRange_AllStartsAtLinkCreation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	created := time.Date(2026, 7, 8, 17, 45, 0, 0, time.UTC)

	w := RangeAll.Window(now, created)

	wantFrom := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	if !w.FromDay.Equal(wantFrom) {
		t.Errorf("FromDay = %v, want the link's creation day %v", w.FromDay, wantFrom)
	}
}

// A backdated seed or a clock skew must not produce FromDay > ToDay, which
// generate_series answers with zero rows and an empty chart.
func TestRange_AllClampsFutureCreationDate(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	created := now.AddDate(0, 0, 5) // link "created" in the future

	w := RangeAll.Window(now, created)

	if w.FromDay.After(w.ToDay) {
		t.Fatalf("FromDay %v is after ToDay %v; the series would be empty", w.FromDay, w.ToDay)
	}
}

// The link's creation timestamp may be in any zone; buckets are always UTC days.
func TestRange_WindowIsUTCRegardlessOfInputZone(t *testing.T) {
	t.Parallel()

	kolkata := time.FixedZone("IST", 5*3600+1800)
	now := time.Date(2026, 7, 10, 2, 0, 0, 0, kolkata) // 2026-07-09 20:30 UTC

	w := Range7d.Window(now, now.AddDate(0, 0, -30))

	if w.ToDay.Location() != time.UTC {
		t.Errorf("ToDay is in %v, want UTC", w.ToDay.Location())
	}
	wantToday := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	if !w.ToDay.Equal(wantToday) {
		t.Errorf("ToDay = %v, want %v (the UTC day, not the local one)", w.ToDay, wantToday)
	}
}
