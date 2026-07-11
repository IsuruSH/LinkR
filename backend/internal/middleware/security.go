package middleware

import (
	"net"
	"net/http"
	"strings"
)

// SecureHeaders sets defensive response headers on every request.
//
// This is an API and a redirector, not an HTML app, so the set is deliberately
// small — no CSP, which belongs on the frontend that actually serves markup.
//
//   - nosniff stops a browser from MIME-sniffing a JSON error body into script.
//   - DENY framing blocks clickjacking; nothing here is meant to be embedded.
//   - no-referrer keeps a long URL's path from leaking to the redirect target
//     via the Referer header.
//
// HSTS is set only in production: a Secure/HSTS policy over plain HTTP would
// break local development, and TLS is terminated by the platform in prod.
func SecureHeaders(isProduction bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			if isProduction {
				h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// MaxBodyBytes caps the request body. Without it, json.NewDecoder reads an
// unbounded stream, so a single large POST can pressure memory. GET and the
// redirect carry no body, so the cap only ever bites a write request.
//
// http.MaxBytesReader (not io.LimitReader) is used because it makes the read
// return an error at the limit rather than silently truncating into malformed
// JSON, and it signals the server to close the connection.
func MaxBodyBytes(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIP resolves the client address for security decisions (rate limiting).
//
// It reads X-Forwarded-For ONLY when trustProxy is set, because that header is
// attacker-controlled when the server is reachable directly. Behind a proxy
// that sets and sanitizes it, the leftmost entry is the real client; otherwise
// the direct peer is the only trustworthy source. This is intentionally stricter
// than the logger's remoteIP, which tolerates spoofing because a forged log line
// is low-stakes and a forged rate-limit key is not.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			if ip := strings.TrimSpace(first); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
