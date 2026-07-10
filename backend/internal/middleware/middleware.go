// Package middleware holds the cross-cutting HTTP concerns that chi does not
// already solve. The chain order in main.go is deliberate:
//
//	RequestID -> Logger -> Recoverer -> Timeout -> CORS -> [Auth]
//
// RequestID first so every later log line and panic report carries one. Logger
// before Recoverer so a panicking request still produces exactly one access log
// with its 500. Timeout inside Recoverer so a timeout cannot skip the panic net.
//
// We use chi's own middleware.Timeout and middleware.WrapResponseWriter rather
// than reimplementing them. What lives here is only what chi gets wrong for us:
//
//   - RequestID: chi's generates a process-local "prefix-counter" ID, which
//     collides across `--scale backend=N`, and it never echoes the header back
//     to the caller. We want a UUID and an X-Request-Id on the response.
//   - Recoverer: chi's writes plain text, and in dev it dumps a stack trace into
//     the response body. Ours writes the JSON error envelope, always.
//   - Logger: chi's is line-oriented text. We emit structured slog.
package middleware

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

type ctxKey int

const (
	requestIDKey ctxKey = iota
	userIDKey
)

const requestIDHeader = "X-Request-Id"

// RequestID honours an inbound X-Request-Id so a trace survives a proxy hop,
// and mints one otherwise.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// RequestIDFrom returns the request ID, or "" when called outside a request.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// Logger emits one structured line per request. Every field the runbook needs:
// request ID, method, path, status, latency, remote IP.
//
// chi's WrapResponseWriter does the capture. It correctly forwards Flush,
// Hijack and Push to the underlying writer, which a naive embedded-struct
// wrapper silently breaks.
func Logger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r)

			status := ww.Status()
			if status == 0 {
				status = http.StatusOK // handler returned without writing
			}
			level := slog.LevelInfo
			switch {
			case status >= 500:
				level = slog.LevelError
			case status >= 400:
				level = slog.LevelWarn
			}

			logger.LogAttrs(r.Context(), level, "http request",
				slog.String("request_id", RequestIDFrom(r.Context())),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", status),
				slog.Int64("latency_ms", time.Since(start).Milliseconds()),
				slog.String("remote_ip", remoteIP(r)),
				slog.Int("bytes", ww.BytesWritten()),
			)
		})
	}
}

// Recoverer converts a panic into a 500 rather than a dropped connection, and
// logs the stack. http.ErrAbortHandler is re-panicked: it is the documented way
// for a handler to abort a connection on purpose, and swallowing it would break
// net/http's own contract.
func Recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				logger.ErrorContext(r.Context(), "panic recovered",
					slog.String("request_id", RequestIDFrom(r.Context())),
					slog.Any("panic", rec),
					slog.String("stack", string(debug.Stack())),
				)
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"code":"INTERNAL","message":"an unexpected error occurred"}}`))
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func remoteIP(r *http.Request) string {
	// Trust X-Forwarded-For only for logging. It is attacker-controlled, so it
	// must never feed authorization or rate-limit keys without a trusted-proxy
	// allowlist in front.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		return strings.TrimSpace(first)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
