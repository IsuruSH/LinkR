package service

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/IsuruSh/linkr/internal/auth"
	"github.com/IsuruSh/linkr/internal/domain"
)

// UserRepo is the persistence surface auth orchestration needs.
type UserRepo interface {
	Create(ctx context.Context, email, passwordHash string) (domain.User, error)
	GetByEmail(ctx context.Context, email string) (domain.User, error)
}

// AuthService orchestrates registration and login. The cryptographic primitives
// live in internal/auth; this package decides when to call them and what a
// failure means to a user.
type AuthService struct {
	users  UserRepo
	issuer *auth.Issuer
}

func NewAuthService(users UserRepo, issuer *auth.Issuer) *AuthService {
	return &AuthService{users: users, issuer: issuer}
}

// Token is what a successful register or login returns.
type Token struct {
	AccessToken string
	ExpiresAt   time.Time
	UserID      uuid.UUID
	Email       string
}

// Register creates an account and immediately issues a token, so the frontend
// does not have to round-trip to /login after signing up.
func (s *AuthService) Register(ctx context.Context, email, password string) (Token, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return Token{}, err
	}
	// Hash before insert: on a duplicate email we have burned ~250ms of bcrypt
	// for nothing, but the alternative — checking existence first — both races
	// and leaks which emails are registered by timing.
	hash, err := auth.HashPassword(password)
	if err != nil {
		return Token{}, err
	}

	user, err := s.users.Create(ctx, email, hash)
	if err != nil {
		return Token{}, err // ErrEmailTaken surfaces as 409
	}
	return s.issue(user)
}

// Login verifies credentials.
//
// A missing user and a wrong password return the identical error. The repository
// already maps "no rows" to ErrInvalidCredentials for this reason: if the two
// differed, /login would tell an attacker which emails have accounts.
//
// The timing side channel is narrower but real: a missing user skips bcrypt and
// returns fast. VerifyPassword is run against a dummy hash in that case so both
// paths pay the same cost.
func (s *AuthService) Login(ctx context.Context, email, password string) (Token, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		// Do not tell the caller their email was malformed on a login attempt;
		// it is the same "invalid credentials" from the outside.
		return Token{}, domain.ErrInvalidCredentials
	}

	user, err := s.users.GetByEmail(ctx, email)
	if err != nil {
		// Equalize timing: hash a throwaway password so a nonexistent account
		// takes as long as a wrong password on a real one.
		auth.VerifyPassword(dummyHash, password)
		return Token{}, domain.ErrInvalidCredentials
	}

	if !auth.VerifyPassword(user.PasswordHash, password) {
		return Token{}, domain.ErrInvalidCredentials
	}
	return s.issue(user)
}

func (s *AuthService) issue(user domain.User) (Token, error) {
	token, expiresAt, err := s.issuer.Issue(user.ID, time.Now())
	if err != nil {
		return Token{}, err
	}
	return Token{
		AccessToken: token,
		ExpiresAt:   expiresAt,
		UserID:      user.ID,
		Email:       user.Email,
	}, nil
}

// dummyHash is a genuine bcrypt hash at the production cost, used only to burn
// equivalent CPU when the account does not exist.
//
// It must be a *parseable* hash. bcrypt.CompareHashAndPassword parses before it
// compares, so a malformed constant would return in microseconds and reopen the
// exact timing channel this closes. TestDummyHashIsParseable guards that.
const dummyHash = "$2a$12$/O8kLZXQ4tsucq/azdJf8ekS4EF.xoks2frQvupSfbvHyHBZNrl9W"

func normalizeEmail(email string) (string, error) {
	email = strings.TrimSpace(email)

	// The column is citext, so casing is already handled by the database. This
	// is a shape check, not a deliverability check: full RFC 5322 validation is
	// a well-known tar pit and rejects addresses that actually work.
	if len(email) < 3 || len(email) > 254 || !strings.Contains(email, "@") {
		return "", domain.NewError(domain.CodeValidation, "email is not valid").
			WithDetails(map[string]string{"email": "must be a valid email address"})
	}
	at := strings.LastIndex(email, "@")
	if at == 0 || at == len(email)-1 || strings.Contains(email, " ") {
		return "", domain.NewError(domain.CodeValidation, "email is not valid").
			WithDetails(map[string]string{"email": "must be a valid email address"})
	}
	return email, nil
}
