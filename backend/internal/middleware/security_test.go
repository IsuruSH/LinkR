package middleware

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func passthrough() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

func TestSecureHeaders(t *testing.T) {
	t.Parallel()

	t.Run("development omits HSTS", func(t *testing.T) {
		rec := httptest.NewRecorder()
		SecureHeaders(false)(passthrough()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
		}
		if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("X-Frame-Options = %q, want DENY", got)
		}
		if rec.Header().Get("Strict-Transport-Security") != "" {
			t.Error("HSTS must not be set in development; it would break plain-HTTP local use")
		}
	})

	t.Run("production sets HSTS", func(t *testing.T) {
		rec := httptest.NewRecorder()
		SecureHeaders(true)(passthrough()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Header().Get("Strict-Transport-Security") == "" {
			t.Error("HSTS must be set in production")
		}
	})
}

// A body over the cap must be rejected, not read into memory. The middleware
// wraps the body; the handler's Read is what trips it.
func TestMaxBodyBytes(t *testing.T) {
	t.Parallel()

	var readErr error
	handler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	})

	body := strings.NewReader(strings.Repeat("a", 100))
	req := httptest.NewRequest(http.MethodPost, "/", body)
	MaxBodyBytes(10)(handler).ServeHTTP(httptest.NewRecorder(), req)

	if readErr == nil {
		t.Fatal("reading a body over the cap should error, but it succeeded")
	}
	var maxErr *http.MaxBytesError
	if !errors.As(readErr, &maxErr) {
		t.Errorf("error = %v, want *http.MaxBytesError", readErr)
	}
}

func TestClientIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		remoteAddr string
		xff        string
		trustProxy bool
		want       string
	}{
		{"no trust ignores XFF", "10.0.0.1:5000", "1.2.3.4", false, "10.0.0.1"},
		{"trust uses leftmost XFF", "10.0.0.1:5000", "1.2.3.4, 5.6.7.8", true, "1.2.3.4"},
		{"trust but no XFF falls back to peer", "10.0.0.1:5000", "", true, "10.0.0.1"},
		{"spoofed XFF ignored without trust", "10.0.0.1:5000", "evil", false, "10.0.0.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			if got := ClientIP(req, tt.trustProxy); got != tt.want {
				t.Errorf("ClientIP = %q, want %q", got, tt.want)
			}
		})
	}
}
