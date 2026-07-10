package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IsuruSh/linkr/internal/domain"
)

func TestError_MapsDomainCodesToStatus(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   domain.Code
	}{
		{"link not found", domain.ErrLinkNotFound, http.StatusNotFound, domain.CodeLinkNotFound},
		{"alias taken", domain.ErrAliasTaken, http.StatusConflict, domain.CodeAliasTaken},
		{"email taken", domain.ErrEmailTaken, http.StatusConflict, domain.CodeEmailTaken},
		{"bad credentials", domain.ErrInvalidCredentials, http.StatusUnauthorized, domain.CodeInvalidCredentials},
		{"unauthorized", domain.ErrUnauthorized, http.StatusUnauthorized, domain.CodeUnauthorized},
		{"invalid url", domain.NewError(domain.CodeInvalidURL, "bad"), http.StatusBadRequest, domain.CodeInvalidURL},
		{"invalid cursor", domain.ErrInvalidCursor, http.StatusBadRequest, domain.CodeInvalidCursor},
		{"codegen failure", domain.NewError(domain.CodeCodeGeneration, "gave up"), http.StatusInternalServerError, domain.CodeCodeGeneration},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			Error(rec, httptest.NewRequest(http.MethodGet, "/", nil), tt.err)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			var env struct {
				Error errorBody `json:"error"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
				t.Fatalf("response is not the error envelope: %v", err)
			}
			if env.Error.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", env.Error.Code, tt.wantCode)
			}
			if env.Error.Message == "" {
				t.Error("message is empty; clients surface this to users")
			}
		})
	}
}

// An error from a lower layer must never reach the client verbatim: a pgx or
// Redis message can leak schema, host names, or query fragments.
func TestError_UnknownErrorIsOpaque(t *testing.T) {
	leaky := errors.New(`pq: relation "users" does not exist at host db-prod-1`)

	rec := httptest.NewRecorder()
	Error(rec, httptest.NewRequest(http.MethodGet, "/", nil), fmt.Errorf("query failed: %w", leaky))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "db-prod-1") || strings.Contains(body, "relation") {
		t.Errorf("internal error leaked to client: %s", body)
	}
}

// Wrapped domain errors keep their identity, so a service can add context with
// %w and still get the right status code.
func TestError_WrappedDomainErrorKeepsStatus(t *testing.T) {
	wrapped := fmt.Errorf("resolving code: %w", domain.ErrLinkNotFound)

	rec := httptest.NewRecorder()
	Error(rec, httptest.NewRequest(http.MethodGet, "/", nil), wrapped)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestErrorIs_ComparesByCode(t *testing.T) {
	// A fresh instance with the same code must satisfy errors.Is against the
	// sentinel; services construct errors with details attached.
	got := domain.NewError(domain.CodeAliasTaken, "alias 'go' is taken").
		WithDetails(map[string]string{"alias": "go"})

	if !errors.Is(got, domain.ErrAliasTaken) {
		t.Error("errors.Is should match on Code, not instance identity")
	}
	if errors.Is(got, domain.ErrLinkNotFound) {
		t.Error("errors.Is matched a different code")
	}
}

func TestJSON_SuccessHasNoEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	JSON(rec, http.StatusCreated, map[string]string{"short_code": "abc1234"})

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, wrapped := body["data"]; wrapped {
		t.Error("success response is wrapped in `data`; the resource should be returned directly")
	}
	if body["short_code"] != "abc1234" {
		t.Errorf("body = %v, want the resource at the top level", body)
	}
}
