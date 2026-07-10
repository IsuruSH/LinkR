package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubPinger struct{ err error }

func (s stubPinger) Ping(context.Context) error { return s.err }

// Liveness must never depend on a dependency. If it did, a Postgres blip would
// make the orchestrator kill every healthy replica — and restarting cannot fix
// someone else's database.
func TestLive_IgnoresDependencies(t *testing.T) {
	t.Parallel()

	h := NewHealthHandler(
		stubPinger{errors.New("postgres is down")},
		stubPinger{errors.New("redis is down")},
		discardLogger(),
	)

	rec := httptest.NewRecorder()
	h.Live(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d with both dependencies down, want 200", rec.Code)
	}
}

// Readiness is where dependencies belong — but they are not equal. Redis being
// down degrades the redirect to a Postgres read, so the replica stays in
// rotation. Postgres being down means we cannot serve at all.
func TestReady_DegradesOnRedisButFailsOnPostgres(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		dbErr      error
		cacheErr   error
		wantStatus int
		wantState  string
		wantChecks map[string]string
	}{
		{
			name: "all healthy", wantStatus: http.StatusOK, wantState: "ready",
			wantChecks: map[string]string{"postgres": "ok", "redis": "ok"},
		},
		{
			// Still 200: the load balancer must keep sending us traffic, because
			// redirects still work without the cache.
			name: "redis down", cacheErr: errors.New("connection refused"),
			wantStatus: http.StatusOK, wantState: "degraded",
			wantChecks: map[string]string{"postgres": "ok", "redis": "unreachable"},
		},
		{
			name: "postgres down", dbErr: errors.New("connection refused"),
			wantStatus: http.StatusServiceUnavailable, wantState: "unavailable",
			wantChecks: map[string]string{"postgres": "unreachable", "redis": "ok"},
		},
		{
			name: "both down", dbErr: errors.New("down"), cacheErr: errors.New("down"),
			wantStatus: http.StatusServiceUnavailable, wantState: "unavailable",
			wantChecks: map[string]string{"postgres": "unreachable", "redis": "unreachable"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := NewHealthHandler(stubPinger{tt.dbErr}, stubPinger{tt.cacheErr}, discardLogger())

			rec := httptest.NewRecorder()
			h.Ready(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}

			var body struct {
				Status string            `json:"status"`
				Checks map[string]string `json:"checks"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.Status != tt.wantState {
				t.Errorf("status = %q, want %q", body.Status, tt.wantState)
			}
			for dep, want := range tt.wantChecks {
				if got := body.Checks[dep]; got != want {
					t.Errorf("checks[%q] = %q, want %q", dep, got, want)
				}
			}
		})
	}
}
