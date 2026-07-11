package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/IsuruSh/linkr/internal/cache"
	"github.com/IsuruSh/linkr/internal/domain"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ---- fakes ------------------------------------------------------------------

type fakeCache struct {
	mu      sync.Mutex
	entries map[string]cache.Entry
	missing map[string]bool

	getErr        error
	setErr        error
	invalidateErr error

	getCalls        int
	setCalls        int
	setMissingCalls int
	invalidateCalls int
}

func newFakeCache() *fakeCache {
	return &fakeCache{entries: map[string]cache.Entry{}, missing: map[string]bool{}}
}

func (f *fakeCache) GetLink(_ context.Context, code string) (cache.Entry, cache.Lookup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	if f.getErr != nil {
		return cache.Entry{}, cache.Miss, f.getErr
	}
	if f.missing[code] {
		return cache.Entry{}, cache.Negative, nil
	}
	if e, ok := f.entries[code]; ok {
		return e, cache.Hit, nil
	}
	return cache.Entry{}, cache.Miss, nil
}

func (f *fakeCache) SetLink(_ context.Context, code string, e cache.Entry, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setCalls++
	if f.setErr != nil {
		return f.setErr
	}
	f.entries[code] = e
	return nil
}

func (f *fakeCache) SetMissing(_ context.Context, code string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setMissingCalls++
	f.missing[code] = true
	return nil
}

func (f *fakeCache) Invalidate(_ context.Context, code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidateCalls++
	if f.invalidateErr != nil {
		return f.invalidateErr
	}
	delete(f.entries, code)
	delete(f.missing, code)
	return nil
}

func (f *fakeCache) Ping(context.Context) error { return nil }
func (f *fakeCache) Close() error               { return nil }

func (f *fakeCache) has(code string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.entries[code]
	return ok
}

func (f *fakeCache) counts() (get, set, setMissing, invalidate int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getCalls, f.setCalls, f.setMissingCalls, f.invalidateCalls
}

type fakeLinkRepo struct {
	mu    sync.Mutex
	links map[string]domain.Link

	getCalls    int
	createCalls int
	deleteCalls int

	// takenCodes forces Create to report a collision for these codes, once each.
	takenCodes map[string]int
	createErr  error
	deleteErr  error
}

func newFakeLinkRepo() *fakeLinkRepo {
	return &fakeLinkRepo{links: map[string]domain.Link{}, takenCodes: map[string]int{}}
}

func (f *fakeLinkRepo) Create(_ context.Context, userID uuid.UUID, code, longURL string, _ *time.Time) (domain.Link, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++

	if f.createErr != nil {
		return domain.Link{}, f.createErr
	}
	if n, ok := f.takenCodes[code]; ok && n > 0 {
		f.takenCodes[code] = n - 1
		return domain.Link{}, domain.ErrAliasTaken
	}
	// "always collide" mode: any code collides.
	if n, ok := f.takenCodes["*"]; ok && n > 0 {
		f.takenCodes["*"] = n - 1
		return domain.Link{}, domain.ErrAliasTaken
	}
	if _, exists := f.links[code]; exists {
		return domain.Link{}, domain.ErrAliasTaken
	}

	l := domain.Link{ID: uuid.New(), UserID: userID, ShortCode: code, LongURL: longURL, CreatedAt: time.Now().UTC()}
	f.links[code] = l
	return l, nil
}

func (f *fakeLinkRepo) GetByShortCode(_ context.Context, code string) (domain.Link, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	if l, ok := f.links[code]; ok {
		return l, nil
	}
	return domain.Link{}, domain.ErrLinkNotFound
}

func (f *fakeLinkRepo) GetByShortCodeForUser(_ context.Context, userID uuid.UUID, code string) (domain.Link, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if l, ok := f.links[code]; ok && l.UserID == userID {
		return l, nil
	}
	return domain.Link{}, domain.ErrLinkNotFound
}

func (f *fakeLinkRepo) DeleteByShortCode(_ context.Context, userID uuid.UUID, code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls++
	if f.deleteErr != nil {
		return f.deleteErr
	}
	l, ok := f.links[code]
	if !ok || l.UserID != userID {
		return domain.ErrLinkNotFound
	}
	delete(f.links, code)
	return nil
}

func (f *fakeLinkRepo) ListPage(context.Context, uuid.UUID, *domain.Cursor, int32) ([]domain.Link, bool, error) {
	return nil, false, nil
}

func (f *fakeLinkRepo) dbReads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getCalls
}

type fakeRecorder struct {
	mu     sync.Mutex
	events []domain.ClickEvent
}

func (f *fakeRecorder) Record(ev domain.ClickEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
}

func (f *fakeRecorder) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

type fakeClickRepo struct{}

func (fakeClickRepo) ClicksPerDay(context.Context, uuid.UUID, domain.StatsWindow) ([]domain.DailyClicks, error) {
	return nil, nil
}

func newTestService(links LinkRepo, c cache.Cache, rec *fakeRecorder) *LinkService {
	return NewLinkService(links, fakeClickRepo{}, c, rec, discardLogger(), 24*time.Hour, time.Minute)
}

// ---- Resolve ----------------------------------------------------------------

// A cache hit must not touch Postgres. If it does, the cache is decorative.
func TestResolve_CacheHitSkipsDatabase(t *testing.T) {
	t.Parallel()

	repo, c := newFakeLinkRepo(), newFakeCache()
	want := cache.Entry{LinkID: uuid.New(), LongURL: "https://example.com"}
	c.entries["abc1234"] = want

	svc := newTestService(repo, c, &fakeRecorder{})
	got, err := svc.Resolve(context.Background(), "abc1234")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != want {
		t.Errorf("entry = %+v, want %+v", got, want)
	}
	if reads := repo.dbReads(); reads != 0 {
		t.Errorf("database was read %d times on a cache hit", reads)
	}
}

// A miss reads Postgres once and fills the cache, so the next request is a hit.
func TestResolve_MissFillsCache(t *testing.T) {
	t.Parallel()

	repo, c := newFakeLinkRepo(), newFakeCache()
	link, _ := repo.Create(context.Background(), uuid.New(), "abc1234", "https://example.com", nil)

	svc := newTestService(repo, c, &fakeRecorder{})

	got, err := svc.Resolve(context.Background(), "abc1234")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.LinkID != link.ID {
		t.Errorf("LinkID = %v, want %v", got.LinkID, link.ID)
	}
	if !c.has("abc1234") {
		t.Fatal("cache was not filled after a miss")
	}

	// Second call is served from cache.
	before := repo.dbReads()
	if _, err := svc.Resolve(context.Background(), "abc1234"); err != nil {
		t.Fatal(err)
	}
	if repo.dbReads() != before {
		t.Error("second Resolve hit the database; the cache fill did not take")
	}
}

// The entry must carry the link ID, not just the URL. Caching the URL alone
// would force a database read on every hit just to record the click.
func TestResolve_CachedEntryCarriesLinkID(t *testing.T) {
	t.Parallel()

	repo, c := newFakeLinkRepo(), newFakeCache()
	link, _ := repo.Create(context.Background(), uuid.New(), "abc1234", "https://example.com", nil)

	svc := newTestService(repo, c, &fakeRecorder{})
	if _, err := svc.Resolve(context.Background(), "abc1234"); err != nil {
		t.Fatal(err)
	}

	c.mu.Lock()
	cached := c.entries["abc1234"]
	c.mu.Unlock()

	if cached.LinkID != link.ID {
		t.Errorf("cached LinkID = %v, want %v", cached.LinkID, link.ID)
	}
	if cached.LinkID == uuid.Nil {
		t.Error("cached entry has no link ID; the click could never be attributed")
	}
}

// An unknown code is negative-cached so an enumeration scan cannot hammer
// Postgres.
func TestResolve_UnknownCodeIsNegativeCached(t *testing.T) {
	t.Parallel()

	repo, c := newFakeLinkRepo(), newFakeCache()
	svc := newTestService(repo, c, &fakeRecorder{})

	_, err := svc.Resolve(context.Background(), "nosuch")
	if !errors.Is(err, domain.ErrLinkNotFound) {
		t.Fatalf("error = %v, want ErrLinkNotFound", err)
	}
	if _, _, setMissing, _ := c.counts(); setMissing != 1 {
		t.Fatalf("SetMissing called %d times, want 1", setMissing)
	}

	// The second lookup must be answered by the negative cache, not Postgres.
	before := repo.dbReads()
	_, err = svc.Resolve(context.Background(), "nosuch")
	if !errors.Is(err, domain.ErrLinkNotFound) {
		t.Fatalf("error = %v, want ErrLinkNotFound", err)
	}
	if repo.dbReads() != before {
		t.Error("a negative-cached miss still queried the database")
	}
}

// Redis being down must not break the redirect. This is the graceful-degradation
// requirement: slow beats broken.
func TestResolve_DegradesToPostgresWhenCacheErrors(t *testing.T) {
	t.Parallel()

	repo, c := newFakeLinkRepo(), newFakeCache()
	link, _ := repo.Create(context.Background(), uuid.New(), "abc1234", "https://example.com", nil)
	c.getErr = errors.New("dial tcp 127.0.0.1:6379: connect: connection refused")

	svc := newTestService(repo, c, &fakeRecorder{})

	got, err := svc.Resolve(context.Background(), "abc1234")
	if err != nil {
		t.Fatalf("Resolve failed while Redis was down; it must degrade to Postgres: %v", err)
	}
	if got.LinkID != link.ID {
		t.Errorf("LinkID = %v, want %v", got.LinkID, link.ID)
	}
}

// ---- Delete / invalidation --------------------------------------------------

func TestDelete_InvalidatesCache(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repo, c := newFakeLinkRepo(), newFakeCache()
	userID := uuid.New()
	link, _ := repo.Create(ctx, userID, "abc1234", "https://example.com", nil)

	svc := newTestService(repo, c, &fakeRecorder{})

	// Warm the cache.
	if _, err := svc.Resolve(ctx, "abc1234"); err != nil {
		t.Fatal(err)
	}
	if !c.has("abc1234") {
		t.Fatal("precondition: cache should be warm")
	}

	if err := svc.Delete(ctx, userID, "abc1234"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if c.has("abc1234") {
		t.Error("cache entry survived the delete; the link would keep redirecting for a full TTL")
	}

	// And a subsequent resolve reports not-found rather than serving the corpse.
	if _, err := svc.Resolve(ctx, "abc1234"); !errors.Is(err, domain.ErrLinkNotFound) {
		t.Errorf("Resolve after delete = %v, want ErrLinkNotFound", err)
	}
	_ = link
}

// If the row was never deleted, the cache must not be invalidated either:
// invalidating first would evict a perfectly good entry on every failed attempt.
func TestDelete_DoesNotInvalidateWhenDeleteFails(t *testing.T) {
	t.Parallel()

	repo, c := newFakeLinkRepo(), newFakeCache()
	svc := newTestService(repo, c, &fakeRecorder{})

	err := svc.Delete(context.Background(), uuid.New(), "nosuch")
	if !errors.Is(err, domain.ErrLinkNotFound) {
		t.Fatalf("error = %v, want ErrLinkNotFound", err)
	}
	if _, _, _, invalidate := c.counts(); invalidate != 0 {
		t.Errorf("Invalidate called %d times after a failed delete, want 0", invalidate)
	}
}

// The row is gone but the cache still holds it: that is a correctness bug, so
// Delete must report it rather than returning nil and leaving a ghost link.
func TestDelete_ReportsInvalidationFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repo, c := newFakeLinkRepo(), newFakeCache()
	userID := uuid.New()
	if _, err := repo.Create(ctx, userID, "abc1234", "https://example.com", nil); err != nil {
		t.Fatal(err)
	}
	c.invalidateErr = errors.New("redis: connection refused")

	svc := newTestService(repo, c, &fakeRecorder{})
	if err := svc.Delete(ctx, userID, "abc1234"); err == nil {
		t.Fatal("Delete returned nil though the cache entry may still serve a deleted link")
	}
}

// ---- Create -----------------------------------------------------------------

func TestCreate_RejectsInvalidURLBeforeTouchingTheDatabase(t *testing.T) {
	t.Parallel()

	repo := newFakeLinkRepo()
	svc := newTestService(repo, newFakeCache(), &fakeRecorder{})

	_, err := svc.Create(context.Background(), uuid.New(), "javascript:alert(1)", "", nil)
	if err == nil {
		t.Fatal("Create accepted a javascript: URL")
	}
	if repo.createCalls != 0 {
		t.Error("validation failed but the repository was still called")
	}
}

// A user-chosen alias that is taken is the user's problem: report the conflict,
// do not silently substitute a generated code.
func TestCreate_TakenAliasIsAConflict(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repo := newFakeLinkRepo()
	if _, err := repo.Create(ctx, uuid.New(), "my-link", "https://other.example", nil); err != nil {
		t.Fatal(err)
	}

	svc := newTestService(repo, newFakeCache(), &fakeRecorder{})
	_, err := svc.Create(ctx, uuid.New(), "https://example.com", "my-link", nil)
	if !errors.Is(err, domain.ErrAliasTaken) {
		t.Fatalf("error = %v, want ErrAliasTaken", err)
	}
}

func TestCreate_RejectsReservedAlias(t *testing.T) {
	t.Parallel()

	svc := newTestService(newFakeLinkRepo(), newFakeCache(), &fakeRecorder{})
	_, err := svc.Create(context.Background(), uuid.New(), "https://example.com", "healthz", nil)

	var derr *domain.Error
	if !errors.As(err, &derr) || derr.Code != domain.CodeReservedAlias {
		t.Fatalf("error = %v, want CodeReservedAlias", err)
	}
}

// A generated code that collides is OUR problem: retry silently.
func TestCreate_RetriesGeneratedCodeOnCollision(t *testing.T) {
	t.Parallel()

	repo := newFakeLinkRepo()
	repo.takenCodes["*"] = 2 // the first two generated codes collide

	svc := newTestService(repo, newFakeCache(), &fakeRecorder{})
	link, err := svc.Create(context.Background(), uuid.New(), "https://example.com", "", nil)
	if err != nil {
		t.Fatalf("Create should have retried past the collisions: %v", err)
	}
	if link.ShortCode == "" {
		t.Error("no short code assigned")
	}
	if repo.createCalls != 3 {
		t.Errorf("Create called %d times, want 3 (two collisions then success)", repo.createCalls)
	}
}

// Retries are bounded. Exhausting them is a 500, not an infinite loop.
func TestCreate_GivesUpAfterMaxAttempts(t *testing.T) {
	t.Parallel()

	repo := newFakeLinkRepo()
	repo.takenCodes["*"] = 1000 // every code collides

	svc := newTestService(repo, newFakeCache(), &fakeRecorder{})
	_, err := svc.Create(context.Background(), uuid.New(), "https://example.com", "", nil)

	var derr *domain.Error
	if !errors.As(err, &derr) || derr.Code != domain.CodeCodeGeneration {
		t.Fatalf("error = %v, want CodeCodeGeneration", err)
	}
	if repo.createCalls != domain.MaxCodeGenerationAttempts {
		t.Errorf("Create called %d times, want %d", repo.createCalls, domain.MaxCodeGenerationAttempts)
	}
}

// ---- RecordClick ------------------------------------------------------------

func TestRecordClick_TruncatesAttackerControlledHeaders(t *testing.T) {
	t.Parallel()

	rec := &fakeRecorder{}
	svc := newTestService(newFakeLinkRepo(), newFakeCache(), rec)

	huge := make([]byte, 10_000)
	for i := range huge {
		huge[i] = 'a'
	}
	svc.RecordClick(uuid.New(), string(huge), string(huge))

	if rec.count() != 1 {
		t.Fatalf("recorded %d events, want 1", rec.count())
	}
	rec.mu.Lock()
	ev := rec.events[0]
	rec.mu.Unlock()

	if len(ev.Referrer) > 512 || len(ev.UserAgent) > 512 {
		t.Errorf("referrer=%d userAgent=%d bytes; unbounded headers let a client write megabytes per click",
			len(ev.Referrer), len(ev.UserAgent))
	}
}
