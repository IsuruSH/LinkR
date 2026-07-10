package repository

import (
	"testing"

	"github.com/google/uuid"

	"github.com/IsuruSh/linkr/internal/domain"
)

func TestAggregateCounts(t *testing.T) {
	t.Parallel()

	a := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
	b := uuid.MustParse("00000000-0000-0000-0000-0000000000bb")
	c := uuid.MustParse("00000000-0000-0000-0000-0000000000cc")

	events := []domain.ClickEvent{
		{LinkID: c}, {LinkID: a}, {LinkID: b}, {LinkID: a}, {LinkID: c}, {LinkID: c},
	}

	ids, counts := aggregateCounts(events)

	if len(ids) != 3 || len(counts) != 3 {
		t.Fatalf("got %d ids and %d counts, want 3 and 3", len(ids), len(counts))
	}

	// Sorted, so concurrent batch transactions request row locks in the same
	// order.
	want := []uuid.UUID{a, b, c}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want sorted %v", ids, want)
		}
	}

	wantCounts := []int64{2, 1, 3}
	for i := range wantCounts {
		if counts[i] != wantCounts[i] {
			t.Errorf("counts[%d] = %d, want %d", i, counts[i], wantCounts[i])
		}
	}
}

// The sort must hold across many runs, not just the one where the map happened
// to iterate in order. Go randomizes map iteration precisely so that code which
// accidentally depends on it fails early.
func TestAggregateCounts_OrderIsStableAcrossRuns(t *testing.T) {
	t.Parallel()

	events := make([]domain.ClickEvent, 0, 12)
	for i := 0; i < 12; i++ {
		events = append(events, domain.ClickEvent{LinkID: uuid.New()})
	}

	first, _ := aggregateCounts(events)
	for run := 0; run < 50; run++ {
		got, _ := aggregateCounts(events)
		for i := range first {
			if got[i] != first[i] {
				t.Fatalf("run %d: order diverged at index %d", run, i)
			}
		}
	}

	for i := 1; i < len(first); i++ {
		if string(first[i-1][:]) >= string(first[i][:]) {
			t.Fatalf("ids are not strictly ascending at index %d", i)
		}
	}
}

func TestAggregateCounts_Empty(t *testing.T) {
	t.Parallel()

	ids, counts := aggregateCounts(nil)
	if len(ids) != 0 || len(counts) != 0 {
		t.Errorf("got %v/%v, want empty", ids, counts)
	}
}

// An absent referrer is SQL NULL, not the empty string. The distinction is what
// makes `WHERE referrer IS NULL` and COUNT(referrer) mean what they say.
func TestNullable(t *testing.T) {
	t.Parallel()

	if got := nullable(""); got != nil {
		t.Errorf("nullable(%q) = %q, want nil", "", *got)
	}
	got := nullable("https://ref.example")
	if got == nil || *got != "https://ref.example" {
		t.Errorf("nullable(non-empty) = %v, want the value back", got)
	}
}
