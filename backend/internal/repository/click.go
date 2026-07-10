package repository

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IsuruSh/linkr/internal/db"
	"github.com/IsuruSh/linkr/internal/domain"
)

// ClickRepository owns the batched write path.
//
// Unlike LinkRepository it needs a *pgxpool.Pool rather than a db.DBTX, because
// it begins its own transaction. Batch writes are the one place a repository
// legitimately controls a transaction boundary: the click rows and the
// denormalized counter must land together or not at all.
type ClickRepository struct {
	pool *pgxpool.Pool
}

// It satisfies worker.BatchInserter. That assertion lives in main.go, where the
// two are wired together: an adapter should not import the package that declares
// the port it fills, or the dependency arrow points the wrong way.
func NewClickRepository(pool *pgxpool.Pool) *ClickRepository {
	return &ClickRepository{pool: pool}
}

// maxInsertAttempts bounds the retry on transient failures. A deadlock loser or
// a serialization failure did nothing wrong; retrying once or twice is free.
// Anything beyond that is a real problem, and the worker counts the batch lost.
const maxInsertAttempts = 3

// InsertBatch writes a batch of clicks in a single transaction:
//
//	COPY into clicks              (one stream, not N round trips)
//	UPDATE links.click_count      (one statement over two arrays)
//
// Both or neither. That is what makes links.click_count trustworthy despite
// being denormalized: it cannot drift from the rows it counts, because no path
// writes one without the other.
func (r *ClickRepository) InsertBatch(ctx context.Context, events []domain.ClickEvent) error {
	if len(events) == 0 {
		return nil
	}

	var lastErr error
	for attempt := 1; attempt <= maxInsertAttempts; attempt++ {
		err := r.insertBatchOnce(ctx, events)
		if err == nil {
			return nil
		}
		lastErr = err

		if !IsRetryable(err) {
			return err
		}
		// Deadlock or serialization failure. Back off briefly; the competing
		// transaction is committing right now.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * 10 * time.Millisecond):
		}
	}
	return fmt.Errorf("insert batch failed after %d attempts: %w", maxInsertAttempts, lastErr)
}

func (r *ClickRepository) insertBatchOnce(ctx context.Context, events []domain.ClickEvent) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning click batch tx: %w", err)
	}
	// Rollback after a successful Commit is a no-op, so this is safe and covers
	// every early return below.
	defer func() { _ = tx.Rollback(ctx) }()

	q := db.New(tx)

	rows := make([]db.BulkInsertClicksParams, len(events))
	for i, ev := range events {
		rows[i] = db.BulkInsertClicksParams{
			LinkID:    ev.LinkID,
			ClickedAt: ev.ClickedAt,
			Referrer:  nullable(ev.Referrer),
			UserAgent: nullable(ev.UserAgent),
		}
	}

	inserted, err := q.BulkInsertClicks(ctx, rows)
	if err != nil {
		return fmt.Errorf("copying clicks: %w", err)
	}
	if inserted != int64(len(rows)) {
		return fmt.Errorf("copied %d clicks, expected %d", inserted, len(rows))
	}

	// Sorted by link ID, so concurrent batches request row locks in a consistent
	// order and narrow the deadlock window.
	ids, counts := aggregateCounts(events)
	if err := q.IncrementClickCounts(ctx, db.IncrementClickCountsParams{
		LinkIds: ids,
		Counts:  counts,
	}); err != nil {
		return fmt.Errorf("incrementing click counts: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing click batch: %w", err)
	}
	return nil
}

// ClicksPerDay returns one row per day in the window, zero-filled.
func (r *ClickRepository) ClicksPerDay(ctx context.Context, linkID uuid.UUID, w domain.StatsWindow) ([]domain.DailyClicks, error) {
	rows, err := db.New(r.pool).GetClicksPerDay(ctx, db.GetClicksPerDayParams{
		FromDay:    w.FromDay,
		ToDay:      w.ToDay,
		LinkID:     linkID,
		RangeStart: w.Start,
		RangeEnd:   w.End,
	})
	if err != nil {
		return nil, fmt.Errorf("querying clicks per day: %w", err)
	}

	out := make([]domain.DailyClicks, len(rows))
	for i, row := range rows {
		out[i] = domain.DailyClicks{Day: row.Day, Clicks: row.Clicks}
	}
	return out, nil
}

// CountForLink reads the raw event table. Used only by integration tests, to
// assert links.click_count has not drifted from reality.
func (r *ClickRepository) CountForLink(ctx context.Context, linkID uuid.UUID) (int64, error) {
	return db.New(r.pool).CountClicksForLink(ctx, linkID)
}

// aggregateCounts collapses a batch into per-link totals, sorted by link ID.
//
// The sort is not cosmetic. Go randomizes map iteration order, so without it two
// workers holding overlapping batches would emit the same links in different
// orders and take row locks in opposing sequences. Sorting gives the common
// plan a consistent lock order. Postgres does not guarantee update order from a
// subquery, so this narrows the deadlock window rather than closing it — and
// InsertBatch retries the loser.
func aggregateCounts(events []domain.ClickEvent) (ids []uuid.UUID, counts []int64) {
	byLink := make(map[uuid.UUID]int64, len(events))
	for _, ev := range events {
		byLink[ev.LinkID]++
	}

	ids = make([]uuid.UUID, 0, len(byLink))
	for id := range byLink {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return string(ids[i][:]) < string(ids[j][:])
	})

	counts = make([]int64, len(ids))
	for i, id := range ids {
		counts[i] = byLink[id]
	}
	return ids, counts
}

// nullable maps "" to SQL NULL. An empty referrer is absent, not empty-string:
// the distinction matters for `WHERE referrer IS NULL` and for COUNT(referrer).
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Compile-time proof that pgx.Tx satisfies the generated DBTX interface, which
// is what lets q := db.New(tx) above share code with db.New(pool).
var _ db.DBTX = (pgx.Tx)(nil)
