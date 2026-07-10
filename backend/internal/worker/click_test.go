package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/IsuruSh/linkr/internal/domain"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeInserter records every batch it is handed. All access is mutex-guarded
// because the worker pool calls InsertBatch from N goroutines concurrently —
// `go test -race` fails loudly otherwise, which is the point.
type fakeInserter struct {
	mu      sync.Mutex
	batches [][]domain.ClickEvent

	// block, when non-nil, stalls InsertBatch until closed. Used to force the
	// buffer to fill.
	block chan struct{}
	// err, when set, makes every insert fail.
	err error

	// called is signalled on each insert so tests can wait on progress instead
	// of sleeping and hoping.
	called chan struct{}
}

func newFakeInserter() *fakeInserter {
	return &fakeInserter{called: make(chan struct{}, 1024)}
}

func (f *fakeInserter) InsertBatch(ctx context.Context, events []domain.ClickEvent) error {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.err != nil {
		return f.err
	}

	// Copy: the worker reuses its batch slice via batch[:0], so retaining the
	// caller's slice would alias memory that is about to be overwritten. A test
	// that skips this copy passes by accident and hides a real aliasing bug.
	cp := make([]domain.ClickEvent, len(events))
	copy(cp, events)

	f.mu.Lock()
	f.batches = append(f.batches, cp)
	f.mu.Unlock()

	select {
	case f.called <- struct{}{}:
	default:
	}
	return nil
}

func (f *fakeInserter) snapshot() [][]domain.ClickEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]domain.ClickEvent, len(f.batches))
	copy(out, f.batches)
	return out
}

func (f *fakeInserter) totalEvents() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, b := range f.batches {
		n += len(b)
	}
	return n
}

func event() domain.ClickEvent {
	return domain.ClickEvent{LinkID: uuid.New(), ClickedAt: time.Now()}
}

// Flush by size: with one worker and BatchSize=10, 10 events must produce
// exactly one insert without waiting for the tick.
func TestClickWorker_FlushesWhenBatchIsFull(t *testing.T) {
	t.Parallel()

	f := newFakeInserter()
	w := New(Config{
		BufferSize:    100,
		Workers:       1,
		BatchSize:     10,
		FlushInterval: time.Hour, // effectively disable the timer
	}, f, discardLogger())
	w.Start()

	for i := 0; i < 10; i++ {
		w.Record(event())
	}

	select {
	case <-f.called:
	case <-time.After(2 * time.Second):
		t.Fatal("no flush within 2s; a full batch must flush without waiting for the tick")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := w.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	batches := f.snapshot()
	if len(batches) != 1 {
		t.Fatalf("got %d batches, want exactly 1", len(batches))
	}
	if len(batches[0]) != 10 {
		t.Errorf("batch size = %d, want 10", len(batches[0]))
	}
}

// Flush by tick: a partial batch must not sit in memory indefinitely.
func TestClickWorker_FlushesOnTickWhenBatchIsPartial(t *testing.T) {
	t.Parallel()

	f := newFakeInserter()
	w := New(Config{
		BufferSize:    100,
		Workers:       1,
		BatchSize:     1000, // never reached
		FlushInterval: 30 * time.Millisecond,
	}, f, discardLogger())
	w.Start()

	w.Record(event())
	w.Record(event())

	select {
	case <-f.called:
	case <-time.After(2 * time.Second):
		t.Fatal("partial batch was never flushed by the ticker")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := w.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if got := f.totalEvents(); got != 2 {
		t.Errorf("persisted %d events, want 2", got)
	}
}

// The drop policy. With the only worker wedged inside InsertBatch and a buffer
// of 2, further Records must return immediately and be counted, never block.
func TestClickWorker_DropsWhenBufferIsFullAndNeverBlocks(t *testing.T) {
	t.Parallel()

	f := newFakeInserter()
	f.block = make(chan struct{})

	w := New(Config{
		BufferSize:    2,
		Workers:       1,
		BatchSize:     1, // first event goes straight into a blocked insert
		FlushInterval: time.Hour,
	}, f, discardLogger())
	w.Start()

	// Wedge the worker: it takes this event and blocks in InsertBatch.
	w.Record(event())

	// Give the worker a moment to actually pick it up and block.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(w.events) > 0 {
		time.Sleep(time.Millisecond)
	}

	// Fill the buffer (2), then overflow it (8 more).
	const overflow = 10
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < overflow; i++ {
			w.Record(event())
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Record blocked when the buffer was full; it must drop instead")
	}

	_, dropped, _ := w.Stats()
	if dropped == 0 {
		t.Fatal("dropped counter is 0; overflowed events must be counted, not silently discarded")
	}
	t.Logf("dropped %d of %d overflow events (buffer=2)", dropped, overflow)

	close(f.block)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = w.Shutdown(ctx)
}

// Graceful shutdown: every event that Record accepted must reach the database
// before Shutdown returns nil. This is the guarantee the redirect path trades
// durability for, so it had better hold.
func TestClickWorker_ShutdownDrainsAcceptedEvents(t *testing.T) {
	t.Parallel()

	f := newFakeInserter()
	w := New(Config{
		BufferSize:    1000,
		Workers:       4,
		BatchSize:     100,
		FlushInterval: time.Hour, // force the drain path, not the ticker, to do the work
	}, f, discardLogger())
	w.Start()

	const n = 500
	for i := 0; i < n; i++ {
		w.Record(event())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// No event was dropped (buffer was large enough), so all n must be persisted.
	written, dropped, lost := w.Stats()
	if dropped != 0 {
		t.Fatalf("dropped = %d, want 0: the buffer was never full", dropped)
	}
	if lost != 0 {
		t.Fatalf("lost = %d, want 0", lost)
	}
	if got := f.totalEvents(); got != n {
		t.Errorf("persisted %d events, want %d: shutdown must drain the buffer", got, n)
	}
	if written != n {
		t.Errorf("written counter = %d, want %d", written, n)
	}
}

// Shutdown must not hang when the database is wedged; it returns the context
// error and the operator learns that events were lost.
func TestClickWorker_ShutdownRespectsDeadline(t *testing.T) {
	t.Parallel()

	f := newFakeInserter()
	f.block = make(chan struct{}) // never released

	w := New(Config{
		BufferSize:    100,
		Workers:       1,
		BatchSize:     1,
		FlushInterval: time.Hour,
		FlushTimeout:  time.Hour, // the insert itself would also hang
	}, f, discardLogger())
	w.Start()

	w.Record(event())

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := w.Shutdown(ctx)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > time.Second {
		t.Errorf("Shutdown took %v; it must be bounded by the context", elapsed)
	}
	close(f.block)
}

// A failed batch increments `lost`, not `written`, and does not take the worker
// down. Analytics degrade; the service does not.
func TestClickWorker_FailedBatchIsCountedAsLost(t *testing.T) {
	t.Parallel()

	f := newFakeInserter()
	f.err = errors.New("connection refused")

	w := New(Config{
		BufferSize:    10,
		Workers:       1,
		BatchSize:     3,
		FlushInterval: time.Hour,
	}, f, discardLogger())
	w.Start()

	for i := 0; i < 3; i++ {
		w.Record(event())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := w.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	written, _, lost := w.Stats()
	if written != 0 {
		t.Errorf("written = %d, want 0: the insert failed", written)
	}
	if lost != 3 {
		t.Errorf("lost = %d, want 3", lost)
	}
}

// Concurrent Record from many goroutines, which is exactly how the redirect
// handler calls it. Run under -race, this is the data-race test.
func TestClickWorker_ConcurrentRecordIsRaceFree(t *testing.T) {
	t.Parallel()

	f := newFakeInserter()
	w := New(Config{
		BufferSize:    5000,
		Workers:       4,
		BatchSize:     50,
		FlushInterval: 10 * time.Millisecond,
	}, f, discardLogger())
	w.Start()

	const goroutines, perGoroutine = 50, 40

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				w.Record(event())
			}
		}()
	}
	wg.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	written, dropped, lost := w.Stats()
	if total := written + dropped + lost; total != goroutines*perGoroutine {
		t.Errorf("written(%d) + dropped(%d) + lost(%d) = %d, want %d: every event must be accounted for",
			written, dropped, lost, total, goroutines*perGoroutine)
	}
}

// Shutdown is called from main's defer path and could plausibly run twice.
// Closing a closed channel panics, so closeOnce is load-bearing.
func TestClickWorker_ShutdownIsIdempotent(t *testing.T) {
	t.Parallel()

	w := New(Config{BufferSize: 10, Workers: 1, BatchSize: 5, FlushInterval: time.Hour},
		newFakeInserter(), discardLogger())
	w.Start()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := w.Shutdown(ctx); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	if err := w.Shutdown(ctx); err != nil { // must not panic
		t.Fatalf("second Shutdown: %v", err)
	}
}
