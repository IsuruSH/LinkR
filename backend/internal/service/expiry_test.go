package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/IsuruSh/linkr/internal/cache"
	"github.com/IsuruSh/linkr/internal/domain"
)

func ptr[T any](v T) *T { return &v }

// effectiveCacheTTL is the clamp that stops a cached entry from outliving the
// link. It is the heart of this slice, so it is tested directly.
func TestEffectiveCacheTTL(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	base := 24 * time.Hour

	tests := []struct {
		name      string
		expiresAt *time.Time
		want      time.Duration
	}{
		{"never expires uses base TTL", nil, base},
		{"expiry beyond base uses base", ptr(now.Add(48 * time.Hour)), base},
		{"expiry within base clamps to expiry", ptr(now.Add(5 * time.Minute)), 5 * time.Minute},
		{"already expired is non-positive", ptr(now.Add(-time.Minute)), -time.Minute},
		{"expires exactly at base boundary", ptr(now.Add(base)), base},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveCacheTTL(base, tt.expiresAt, now); got != tt.want {
				t.Errorf("effectiveCacheTTL = %v, want %v", got, tt.want)
			}
		})
	}
}

// An expired link resolves to ErrLinkExpired (which the handler maps to 410),
// on the Postgres path.
func TestResolve_ExpiredLinkReturnsExpired(t *testing.T) {
	t.Parallel()

	repo, c := newFakeLinkRepo(), newFakeCache()
	past := time.Now().UTC().Add(-time.Hour)
	// Seed the repo with an already-expired link.
	repo.links["dead"] = domain.Link{
		ID: uuid.New(), ShortCode: "dead", LongURL: "https://example.com", ExpiresAt: &past,
	}

	svc := newTestService(repo, c, &fakeRecorder{})
	_, err := svc.Resolve(context.Background(), "dead")
	if !errors.Is(err, domain.ErrLinkExpired) {
		t.Fatalf("Resolve = %v, want ErrLinkExpired", err)
	}
}

// An expired link is NOT negative-cached: doing so would flip its error from
// 410 Expired to 404 Not-Found on the next hit, losing the distinction the
// friendly page relies on. Every hit returns a consistent ErrLinkExpired.
func TestResolve_ExpiredLinkIsNotNegativeCached(t *testing.T) {
	t.Parallel()

	repo, c := newFakeLinkRepo(), newFakeCache()
	past := time.Now().UTC().Add(-time.Minute)
	repo.links["dead"] = domain.Link{ID: uuid.New(), ShortCode: "dead", LongURL: "https://x.example", ExpiresAt: &past}

	svc := newTestService(repo, c, &fakeRecorder{})

	// Two hits, both must report expired — not found on the second.
	for i := 0; i < 2; i++ {
		if _, err := svc.Resolve(context.Background(), "dead"); !errors.Is(err, domain.ErrLinkExpired) {
			t.Fatalf("Resolve #%d = %v, want ErrLinkExpired", i+1, err)
		}
	}
	if _, _, setMissing, _ := c.counts(); setMissing != 0 {
		t.Errorf("SetMissing called %d times, want 0: an expired link must stay 410, not become 404", setMissing)
	}
}

// A cache hit for an entry that has since expired must still return 410, not the
// stale URL. The clamp makes this rare, but clock skew can produce it.
func TestResolve_ExpiredCacheHitReturnsExpired(t *testing.T) {
	t.Parallel()

	repo, c := newFakeLinkRepo(), newFakeCache()
	past := time.Now().UTC().Add(-time.Second)
	c.entries["stale"] = cache.Entry{LinkID: uuid.New(), LongURL: "https://x.example", ExpiresAt: &past}

	svc := newTestService(repo, c, &fakeRecorder{})
	_, err := svc.Resolve(context.Background(), "stale")
	if !errors.Is(err, domain.ErrLinkExpired) {
		t.Fatalf("Resolve = %v, want ErrLinkExpired from an expired cache hit", err)
	}
	// It must not have touched Postgres — expiry is enforced from the entry alone.
	if repo.dbReads() != 0 {
		t.Error("an expired cache hit fell through to the database")
	}
}

// A link expiring within the cache TTL must be cached only until it expires, not
// for the full base TTL.
func TestResolve_CacheFillClampsTTLToExpiry(t *testing.T) {
	t.Parallel()

	repo, c := newFakeLinkRepo(), newFakeCache()
	soon := time.Now().UTC().Add(2 * time.Minute)
	repo.links["soon"] = domain.Link{ID: uuid.New(), ShortCode: "soon", LongURL: "https://x.example", ExpiresAt: &soon}

	svc := newTestService(repo, c, &fakeRecorder{})
	if _, err := svc.Resolve(context.Background(), "soon"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// The entry is cached, and it carries the expiry so a subsequent hit can
	// enforce it.
	c.mu.Lock()
	entry, cached := c.entries["soon"]
	c.mu.Unlock()
	if !cached {
		t.Fatal("a not-yet-expired link should be cached")
	}
	if entry.ExpiresAt == nil {
		t.Error("cached entry did not carry ExpiresAt; a hit could not enforce expiry")
	}
}

// A valid future expiry passes; a past or absurdly distant one is rejected on
// create.
func TestCreate_ValidatesExpiry(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	tests := []struct {
		name    string
		exp     *time.Time
		wantErr bool
	}{
		{"no expiry", nil, false},
		{"future", ptr(now.Add(time.Hour)), false},
		{"past", ptr(now.Add(-time.Hour)), true},
		{"too far", ptr(now.Add(domain.MaxExpiryHorizon + 24*time.Hour)), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService(newFakeLinkRepo(), newFakeCache(), &fakeRecorder{})
			_, err := svc.Create(context.Background(), uuid.New(), "https://example.com", "", tt.exp)
			if tt.wantErr != (err != nil) {
				t.Fatalf("Create with expiry %v: err = %v, wantErr %v", tt.exp, err, tt.wantErr)
			}
		})
	}
}
