package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/IsuruSh/linkr/internal/cache"
	"github.com/IsuruSh/linkr/internal/domain"
	"github.com/IsuruSh/linkr/internal/middleware"
	"github.com/IsuruSh/linkr/internal/service"
)

const (
	baseURL = "http://localhost:8080"
	appURL  = "http://localhost:3000"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ---- fakes ------------------------------------------------------------------

type stubCache struct {
	entry cache.Entry
	found bool
}

func (s stubCache) GetLink(context.Context, string) (cache.Entry, cache.Lookup, error) {
	if s.found {
		return s.entry, cache.Hit, nil
	}
	return cache.Entry{}, cache.Miss, nil
}
func (stubCache) SetLink(context.Context, string, cache.Entry, time.Duration) error { return nil }
func (stubCache) SetMissing(context.Context, string, time.Duration) error           { return nil }
func (stubCache) Invalidate(context.Context, string) error                          { return nil }
func (stubCache) Ping(context.Context) error                                        { return nil }
func (stubCache) Close() error                                                      { return nil }

type stubLinkRepo struct {
	link  domain.Link
	found bool
}

func (s stubLinkRepo) Create(context.Context, uuid.UUID, string, string, *time.Time) (domain.Link, error) {
	return s.link, nil
}
func (s stubLinkRepo) GetByShortCode(context.Context, string) (domain.Link, error) {
	if s.found {
		return s.link, nil
	}
	return domain.Link{}, domain.ErrLinkNotFound
}
func (s stubLinkRepo) GetByShortCodeForUser(context.Context, uuid.UUID, string) (domain.Link, error) {
	if s.found {
		return s.link, nil
	}
	return domain.Link{}, domain.ErrLinkNotFound
}
func (s stubLinkRepo) DeleteByShortCode(context.Context, uuid.UUID, string) error { return nil }
func (s stubLinkRepo) ListPage(context.Context, uuid.UUID, *domain.Cursor, int32) ([]domain.Link, bool, error) {
	return nil, false, nil
}

type stubClickRepo struct{}

func (stubClickRepo) ClicksPerDay(context.Context, uuid.UUID, domain.StatsWindow) ([]domain.DailyClicks, error) {
	return nil, nil
}

// orderingRecorder asserts, at the moment Record is called, that the HTTP
// response has already been written. That is the async guarantee: the browser is
// on its way before we touch the click pipeline.
type orderingRecorder struct {
	mu               sync.Mutex
	calls            int
	statusWhenCalled int
	rec              *httptest.ResponseRecorder
}

func (o *orderingRecorder) Record(domain.ClickEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	o.statusWhenCalled = o.rec.Code
}

// ---- tests ------------------------------------------------------------------

func TestRedirect_Writes302BeforeRecordingTheClick(t *testing.T) {
	link := domain.Link{ID: uuid.New(), ShortCode: "abc1234", LongURL: "https://example.com/target"}

	rec := httptest.NewRecorder()
	recorder := &orderingRecorder{rec: rec}

	svc := service.NewLinkService(
		stubLinkRepo{link: link, found: true},
		stubClickRepo{},
		stubCache{entry: cache.Entry{LinkID: link.ID, LongURL: link.LongURL}, found: true},
		recorder,
		discardLogger(),
		time.Hour, time.Minute,
	)

	r := chi.NewRouter()
	r.Get("/{code}", NewLinkHandler(svc, baseURL, appURL).Redirect)
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/abc1234", nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != link.LongURL {
		t.Errorf("Location = %q, want %q", got, link.LongURL)
	}

	recorder.mu.Lock()
	calls, statusWhenCalled := recorder.calls, recorder.statusWhenCalled
	recorder.mu.Unlock()

	if calls != 1 {
		t.Fatalf("Record called %d times, want 1", calls)
	}
	// The assertion that matters: the 302 was already on the wire.
	if statusWhenCalled != http.StatusFound {
		t.Errorf("response status was %d when the click was recorded; the click must not precede the redirect",
			statusWhenCalled)
	}
}

// Without no-store the browser caches the redirect and stops calling us, and the
// click count silently freezes. This is the most common way analytics on a
// shortener quietly break.
func TestRedirect_IsNotCacheable(t *testing.T) {
	link := domain.Link{ID: uuid.New(), ShortCode: "abc1234", LongURL: "https://example.com/target"}

	rec := httptest.NewRecorder()
	svc := service.NewLinkService(
		stubLinkRepo{link: link, found: true}, stubClickRepo{},
		stubCache{entry: cache.Entry{LinkID: link.ID, LongURL: link.LongURL}, found: true},
		&orderingRecorder{rec: rec}, discardLogger(), time.Hour, time.Minute,
	)

	r := chi.NewRouter()
	r.Get("/{code}", NewLinkHandler(svc, baseURL, appURL).Redirect)
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/abc1234", nil))

	cc := rec.Header().Get("Cache-Control")
	if cc == "" {
		t.Fatal("no Cache-Control header; the browser will cache the 302 and clicks will stop being counted")
	}
	if rec.Code == http.StatusMovedPermanently {
		t.Error("301 is cached indefinitely and effectively irreversible; use 302")
	}
}

// An unknown code is a 404 in the standard envelope, and no click is recorded.
func TestRedirect_UnknownCodeIsNotFoundAndRecordsNothing(t *testing.T) {
	rec := httptest.NewRecorder()
	recorder := &orderingRecorder{rec: rec}

	svc := service.NewLinkService(
		stubLinkRepo{found: false}, stubClickRepo{}, stubCache{found: false},
		recorder, discardLogger(), time.Hour, time.Minute,
	)

	r := chi.NewRouter()
	r.Get("/{code}", NewLinkHandler(svc, baseURL, appURL).Redirect)
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nosuch", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("404 body is not the error envelope: %v", err)
	}
	if env.Error.Code != string(domain.CodeLinkNotFound) {
		t.Errorf("error code = %q, want %q", env.Error.Code, domain.CodeLinkNotFound)
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.calls != 0 {
		t.Errorf("recorded %d clicks for a nonexistent link", recorder.calls)
	}
}

// A protected handler reached without RequireAuth must fail closed, not act as
// the nil user. One forgotten middleware line should not expose every account.
func TestCreate_WithoutAuthContextFailsClosed(t *testing.T) {
	svc := service.NewLinkService(
		stubLinkRepo{}, stubClickRepo{}, stubCache{},
		&orderingRecorder{rec: httptest.NewRecorder()}, discardLogger(), time.Hour, time.Minute,
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/links", nil)
	NewLinkHandler(svc, baseURL, appURL).Create(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 when the user ID is absent from context", rec.Code)
	}
}

func TestCreate_ReturnsCreatedWithShortURL(t *testing.T) {
	userID := uuid.New()
	link := domain.Link{
		ID: uuid.New(), UserID: userID, ShortCode: "abc1234",
		LongURL: "https://example.com", CreatedAt: time.Now().UTC(),
	}

	svc := service.NewLinkService(
		stubLinkRepo{link: link}, stubClickRepo{}, stubCache{},
		&orderingRecorder{rec: httptest.NewRecorder()}, discardLogger(), time.Hour, time.Minute,
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/links",
		strings.NewReader(`{"url":"https://example.com"}`))
	req = req.WithContext(middleware.WithUserID(req.Context(), userID))

	NewLinkHandler(svc, baseURL, appURL).Create(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201. body: %s", rec.Code, rec.Body.String())
	}

	var got linkResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ShortURL != baseURL+"/abc1234" {
		t.Errorf("short_url = %q, want %q", got.ShortURL, baseURL+"/abc1234")
	}
}

func TestParseLimit(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", domain.DefaultPageSize},
		{"abc", domain.DefaultPageSize},
		{"-5", domain.DefaultPageSize},
		{"10", 10},
		{"100", 100},
		{"999999", domain.MaxPageSize},
		{"1e9", domain.DefaultPageSize},
	}
	for _, tt := range tests {
		if got := parseLimit(tt.in); got != tt.want {
			t.Errorf("parseLimit(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
