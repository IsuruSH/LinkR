// Package worker owns the async click path.
//
// It is the boundary that makes the redirect fast: the handler hands over a
// ClickEvent and returns a 302 immediately, never waiting on Postgres. It is
// also the seam for the next scaling step — replacing the in-process channel
// with Redis Streams or NATS changes this package and nothing else, because
// handlers only ever see the Recorder interface.
package worker

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IsuruSh/linkr/internal/domain"
)

// Recorder is all the handler knows about click recording. Swapping the
// implementation for a Redis Streams producer does not touch handler code.
type Recorder interface {
	Record(domain.ClickEvent)
}

// BatchInserter persists one batch atomically. Implemented by the click
// repository; faked in tests, which is why the worker never sees a *pgxpool.Pool.
type BatchInserter interface {
	InsertBatch(ctx context.Context, events []domain.ClickEvent) error
}

type Config struct {
	BufferSize    int
	Workers       int
	BatchSize     int
	FlushInterval time.Duration
	// FlushTimeout bounds a single database write. It is separate from the
	// request timeout: no request is waiting on this.
	FlushTimeout time.Duration
}

// ClickWorker fans a bounded channel out to a small pool of goroutines, each
// accumulating events and writing them in batches.
type ClickWorker struct {
	events   chan domain.ClickEvent
	inserter BatchInserter
	cfg      Config
	logger   *slog.Logger

	wg        sync.WaitGroup
	closeOnce sync.Once

	// Counters, not metrics. Prometheus reads these; see DECISIONS.md for the
	// insertion point. atomic because Record runs on every redirect goroutine.
	dropped atomic.Int64
	lost    atomic.Int64
	written atomic.Int64

	// lastDropLogUnixNano rate-limits the "buffer full" warning. Under sustained
	// overload the buffer is full on every request, and logging each one turns a
	// capacity problem into a disk-and-CPU problem.
	lastDropLogUnixNano atomic.Int64
}

var _ Recorder = (*ClickWorker)(nil)

func New(cfg Config, inserter BatchInserter, logger *slog.Logger) *ClickWorker {
	if cfg.FlushTimeout <= 0 {
		cfg.FlushTimeout = 5 * time.Second
	}
	return &ClickWorker{
		events:   make(chan domain.ClickEvent, cfg.BufferSize),
		inserter: inserter,
		cfg:      cfg,
		logger:   logger,
	}
}

// Start launches the pool. Call once.
func (w *ClickWorker) Start() {
	for i := 0; i < w.cfg.Workers; i++ {
		w.wg.Add(1)
		go w.loop(i)
	}
	w.logger.Info("click worker started",
		"workers", w.cfg.Workers,
		"buffer_size", w.cfg.BufferSize,
		"batch_size", w.cfg.BatchSize,
		"flush_interval", w.cfg.FlushInterval,
	)
}

// Record enqueues an event. It never blocks.
//
// When the buffer is full we drop the event and count it. The alternative is to
// block the redirect, which converts a write-path backlog into a user-facing
// outage: the database is already behind, and queueing HTTP handlers behind it
// makes the outage worse and longer. Clicks are analytics, not billing.
//
// Precondition: Record is never called after Shutdown. main.go guarantees this
// by calling srv.Shutdown first — once it returns, no handler goroutine exists
// to call Record, so closing the channel cannot race with a send. Enforcing this
// with a mutex would put a lock on the hot path to defend against a bug that the
// shutdown ordering already makes impossible.
func (w *ClickWorker) Record(ev domain.ClickEvent) {
	select {
	case w.events <- ev:
	default:
		w.dropped.Add(1)
		w.logDropThrottled()
	}
}

func (w *ClickWorker) logDropThrottled() {
	const interval = int64(time.Second)

	now := time.Now().UnixNano()
	last := w.lastDropLogUnixNano.Load()
	if now-last < interval {
		return
	}
	// CompareAndSwap: exactly one goroutine wins the right to log this second.
	if !w.lastDropLogUnixNano.CompareAndSwap(last, now) {
		return
	}
	w.logger.Warn("click buffer full, dropping events",
		"clicks_dropped_total", w.dropped.Load(),
		"buffer_size", w.cfg.BufferSize,
		"hint", "the database cannot keep up, or CLICK_WORKERS is too low",
	)
}

// Shutdown closes the channel and waits for the pool to drain, bounded by ctx.
//
// Returning ctx.Err() means the deadline hit with events still buffered: those
// are lost. That is a deliberate bound — a shutdown that waits forever on a sick
// database never completes, and the orchestrator SIGKILLs us anyway.
func (w *ClickWorker) Shutdown(ctx context.Context) error {
	w.closeOnce.Do(func() { close(w.events) })

	drained := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(drained)
	}()

	select {
	case <-drained:
		w.logger.Info("click worker drained",
			"clicks_written_total", w.written.Load(),
			"clicks_dropped_total", w.dropped.Load(),
			"clicks_lost_total", w.lost.Load(),
		)
		return nil
	case <-ctx.Done():
		w.logger.Error("click worker drain timed out; buffered events lost",
			"remaining", len(w.events),
			"clicks_dropped_total", w.dropped.Load(),
		)
		return ctx.Err()
	}
}

// Stats exposes the counters. Prometheus hooks here; tests assert on it.
func (w *ClickWorker) Stats() (written, dropped, lost int64) {
	return w.written.Load(), w.dropped.Load(), w.lost.Load()
}

func (w *ClickWorker) loop(id int) {
	defer w.wg.Done()

	batch := make([]domain.ClickEvent, 0, w.cfg.BatchSize)
	ticker := time.NewTicker(w.cfg.FlushInterval)
	defer ticker.Stop()

	for {
		select {
		case ev, open := <-w.events:
			if !open {
				// The channel is closed AND drained: Go delivers every buffered
				// value before reporting !open. Flush what we hold and exit.
				w.flush(batch)
				return
			}

			batch = append(batch, ev)
			if len(batch) >= w.cfg.BatchSize {
				w.flush(batch)
				batch = batch[:0]
				// Reset so a size-triggered flush does not leave a stale tick
				// that immediately fires on a nearly-empty batch.
				ticker.Reset(w.cfg.FlushInterval)
			}

		case <-ticker.C:
			// Time-based flush bounds the staleness of a partial batch. Without
			// it, a link with 3 clicks an hour would sit in memory indefinitely.
			if len(batch) > 0 {
				w.flush(batch)
				batch = batch[:0]
			}
		}
	}
}

// flush writes one batch. It never returns an error: there is no caller to
// return it to. A failed batch is counted and logged, and the events are gone.
func (w *ClickWorker) flush(batch []domain.ClickEvent) {
	if len(batch) == 0 {
		return
	}

	// A fresh context: the request that produced these events completed long ago,
	// and its context is cancelled. Bounding it separately is what keeps a slow
	// database from wedging a worker forever.
	ctx, cancel := context.WithTimeout(context.Background(), w.cfg.FlushTimeout)
	defer cancel()

	if err := w.inserter.InsertBatch(ctx, batch); err != nil {
		w.lost.Add(int64(len(batch)))
		w.logger.Error("click batch insert failed; events lost",
			"error", err,
			"batch_size", len(batch),
			"clicks_lost_total", w.lost.Load(),
		)
		return
	}
	w.written.Add(int64(len(batch)))
}
