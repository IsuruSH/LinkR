// Package auth handles password hashing and JWT issue/verify. It knows nothing
// about HTTP; the middleware that guards routes lives in internal/middleware.
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/IsuruSh/linkr/internal/domain"
)

// bcryptCost 12 is roughly 250ms on a modern core. Login is not a hot path, and
// the cost is the only thing standing between a leaked table and a wordlist.
//
// argon2id resists GPU cracking better and would be the choice for a product
// storing real credentials. bcrypt wins here on one axis: it has a single tuning
// knob that cannot be misconfigured into uselessness, whereas argon2's
// memory/parallelism/time triple is easy to set wrong. Documented in DECISIONS.md.
const bcryptCost = 12

// bcrypt silently truncates input at 72 bytes. A 100-character passphrase and
// its 72-character prefix would hash identically, so reject rather than truncate.
const maxPasswordBytes = 72

const minPasswordLength = 8

// HashPassword returns a bcrypt hash. The cost and salt are embedded in the
// output, so verification needs no side channel.
func HashPassword(plain string) (string, error) {
	if err := ValidatePassword(plain); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("hashing password: %w", err)
	}
	return string(hash), nil
}

// ValidatePassword enforces the bounds bcrypt imposes plus a sane minimum.
func ValidatePassword(plain string) error {
	if len(plain) < minPasswordLength {
		return domain.NewError(domain.CodeValidation, "password is too short").
			WithDetails(map[string]string{"password": fmt.Sprintf("must be at least %d characters", minPasswordLength)})
	}
	if len(plain) > maxPasswordBytes {
		return domain.NewError(domain.CodeValidation, "password is too long").
			WithDetails(map[string]string{"password": fmt.Sprintf("must be at most %d bytes", maxPasswordBytes)})
	}
	return nil
}

// VerifyPassword reports whether plain matches hash.
//
// bcrypt.CompareHashAndPassword is constant-time with respect to the hash, so
// this does not leak the password by timing. It returns a bool rather than an
// error so callers cannot accidentally treat "mismatch" as a server error.
func VerifyPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// Claims is the JWT payload. Subject carries the user ID.
type Claims struct {
	jwt.RegisteredClaims
}

// Issuer signs and verifies tokens. One instance, built in main.go from config.
type Issuer struct {
	secret []byte
	ttl    time.Duration
}

func NewIssuer(secret string, ttl time.Duration) *Issuer {
	return &Issuer{secret: []byte(secret), ttl: ttl}
}

// Issue mints a token for a user.
func (i *Issuer) Issue(userID uuid.UUID, now time.Time) (string, time.Time, error) {
	expiresAt := now.Add(i.ttl)

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			Issuer:    "linkr",
		},
	})

	signed, err := token.SignedString(i.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("signing token: %w", err)
	}
	return signed, expiresAt, nil
}

// Verify parses a token and returns the user ID it asserts.
//
// The signing method is pinned. Without this check an attacker submits a token
// with alg=none, or alg=HS256 signed with our public key if we ever moved to
// RS256 — the classic JWT algorithm-confusion attack. jwt/v5 requires the
// explicit WithValidMethods option; it does not infer safety for you.
func (i *Issuer) Verify(tokenString string) (uuid.UUID, error) {
	token, err := jwt.ParseWithClaims(
		tokenString,
		&Claims{},
		func(t *jwt.Token) (any, error) { return i.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer("linkr"),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		// Every failure — expired, bad signature, malformed, wrong alg — is one
		// error to the client. Distinguishing them tells an attacker which half
		// of a forged token to keep working on.
		switch {
		case errors.Is(err, jwt.ErrTokenExpired):
			return uuid.Nil, domain.NewError(domain.CodeUnauthorized, "token has expired")
		default:
			return uuid.Nil, domain.ErrUnauthorized
		}
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || claims.Subject == "" {
		return uuid.Nil, domain.ErrUnauthorized
	}

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return uuid.Nil, domain.ErrUnauthorized
	}
	return userID, nil
}

// TTL exposes the token lifetime so the login handler can tell the frontend when
// to expire its cookie, keeping the two from disagreeing.
func (i *Issuer) TTL() time.Duration { return i.ttl }
