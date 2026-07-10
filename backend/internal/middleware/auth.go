package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/IsuruSh/linkr/internal/domain"
	"github.com/IsuruSh/linkr/internal/httpx"
)

// TokenVerifier is satisfied by *auth.Issuer. Declared here so middleware does
// not import the auth package's concrete type, and so tests can inject a stub.
type TokenVerifier interface {
	Verify(token string) (uuid.UUID, error)
}

// RequireAuth rejects a request without a valid bearer token and otherwise puts
// the authenticated user ID into the request context.
//
// It is applied per-route-group in main.go rather than globally, because the
// redirect endpoint is public. Mounting auth globally and then punching holes in
// it is how public endpoints accidentally become private, or worse, the reverse.
func RequireAuth(verifier TokenVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearerToken(r)
			if !ok {
				httpx.Error(w, r, domain.ErrUnauthorized)
				return
			}

			userID, err := verifier.Verify(token)
			if err != nil {
				httpx.Error(w, r, err)
				return
			}

			ctx := context.WithValue(r.Context(), userIDKey, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserIDFrom returns the authenticated user. It returns an error rather than a
// zero UUID when absent: a handler that forgets RequireAuth then fails closed
// with a 401 instead of silently operating as the nil user, which would let one
// missing middleware line expose every other user's links.
func UserIDFrom(ctx context.Context) (uuid.UUID, error) {
	id, ok := ctx.Value(userIDKey).(uuid.UUID)
	if !ok || id == uuid.Nil {
		return uuid.Nil, domain.ErrUnauthorized
	}
	return id, nil
}

// WithUserID injects a user ID. Test-only helper, kept beside the reader so the
// context key stays unexported.
func WithUserID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, userIDKey, id)
}

func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", false
	}
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}
