package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/IsuruSh/linkr/internal/auth"
	"github.com/IsuruSh/linkr/internal/domain"
)

type fakeUserRepo struct {
	users map[string]domain.User
}

func newFakeUserRepo() *fakeUserRepo { return &fakeUserRepo{users: map[string]domain.User{}} }

func (f *fakeUserRepo) Create(_ context.Context, email, hash string) (domain.User, error) {
	if _, exists := f.users[email]; exists {
		return domain.User{}, domain.ErrEmailTaken
	}
	u := domain.User{Email: email, PasswordHash: hash, CreatedAt: time.Now()}
	f.users[email] = u
	return u, nil
}

func (f *fakeUserRepo) GetByEmail(_ context.Context, email string) (domain.User, error) {
	if u, ok := f.users[email]; ok {
		return u, nil
	}
	// Mirrors the real repository: a missing user is invalid credentials, not
	// a distinguishable "not found".
	return domain.User{}, domain.ErrInvalidCredentials
}

func newAuthService() *AuthService {
	return NewAuthService(newFakeUserRepo(), auth.NewIssuer("a-test-secret-of-at-least-32-bytes!!", time.Hour))
}

// dummyHash must be a real, parseable bcrypt hash. If it is not,
// CompareHashAndPassword bails on the parse in microseconds and the timing
// channel that Login closes is silently reopened.
func TestDummyHashIsParseable(t *testing.T) {
	t.Parallel()

	cost, err := bcrypt.Cost([]byte(dummyHash))
	if err != nil {
		t.Fatalf("dummyHash is not a valid bcrypt hash: %v — the login timing equalization is a no-op", err)
	}
	if cost < 10 {
		t.Errorf("dummyHash cost = %d; too cheap to match a real hash's timing", cost)
	}
	// It must never accidentally match anything a user might type.
	if auth.VerifyPassword(dummyHash, "") || auth.VerifyPassword(dummyHash, "password") {
		t.Error("dummyHash matched a plausible password")
	}
}

func TestRegister_ThenLogin(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc := newAuthService()

	tok, err := svc.Register(ctx, "user@example.com", "a-good-password")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if tok.AccessToken == "" {
		t.Error("Register returned no token; the client would have to log in again immediately")
	}

	got, err := svc.Login(ctx, "user@example.com", "a-good-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got.UserID != tok.UserID {
		t.Error("login returned a different user")
	}
}

func TestRegister_DuplicateEmailIsConflict(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc := newAuthService()

	if _, err := svc.Register(ctx, "dupe@example.com", "a-good-password"); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Register(ctx, "dupe@example.com", "another-password")
	if !errors.Is(err, domain.ErrEmailTaken) {
		t.Fatalf("error = %v, want ErrEmailTaken", err)
	}
}

// The endpoint must not become a user-enumeration oracle: an unknown email and a
// wrong password are indistinguishable from the outside.
func TestLogin_UnknownEmailAndWrongPasswordAreIndistinguishable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc := newAuthService()
	if _, err := svc.Register(ctx, "real@example.com", "the-real-password"); err != nil {
		t.Fatal(err)
	}

	_, errUnknown := svc.Login(ctx, "ghost@example.com", "any-password")
	_, errWrongPw := svc.Login(ctx, "real@example.com", "wrong-password")

	if !errors.Is(errUnknown, domain.ErrInvalidCredentials) {
		t.Errorf("unknown email error = %v, want ErrInvalidCredentials", errUnknown)
	}
	if !errors.Is(errWrongPw, domain.ErrInvalidCredentials) {
		t.Errorf("wrong password error = %v, want ErrInvalidCredentials", errWrongPw)
	}
	if errUnknown.Error() != errWrongPw.Error() {
		t.Errorf("the two errors differ (%q vs %q); the response reveals whether an account exists",
			errUnknown, errWrongPw)
	}
}

// A malformed email on LOGIN must also be generic. Reporting "invalid email
// format" would confirm which strings are not accounts.
func TestLogin_MalformedEmailIsGenericError(t *testing.T) {
	t.Parallel()

	_, err := newAuthService().Login(context.Background(), "not-an-email", "whatever")
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("error = %v, want ErrInvalidCredentials", err)
	}
}

// On REGISTER, by contrast, a bad email is a real validation error: the user is
// creating the account and needs to know what is wrong.
func TestRegister_MalformedEmailIsValidationError(t *testing.T) {
	t.Parallel()

	tests := []string{"", "no-at-sign", "@leading", "trailing@", "with space@example.com"}
	for _, email := range tests {
		_, err := newAuthService().Register(context.Background(), email, "a-good-password")
		var derr *domain.Error
		if !errors.As(err, &derr) || derr.Code != domain.CodeValidation {
			t.Errorf("Register(%q) error = %v, want CodeValidation", email, err)
		}
	}
}

func TestRegister_RejectsWeakPassword(t *testing.T) {
	t.Parallel()

	_, err := newAuthService().Register(context.Background(), "user@example.com", "short")
	var derr *domain.Error
	if !errors.As(err, &derr) || derr.Code != domain.CodeValidation {
		t.Fatalf("error = %v, want CodeValidation", err)
	}
}

func TestNormalizeEmail(t *testing.T) {
	t.Parallel()

	if got, err := normalizeEmail("  user@example.com  "); err != nil || got != "user@example.com" {
		t.Errorf("normalizeEmail trimmed to %q, err=%v", got, err)
	}
	// Casing is handled by citext in the database, so it is preserved here.
	if got, err := normalizeEmail("User@Example.com"); err != nil || got != "User@Example.com" {
		t.Errorf("normalizeEmail = %q, err=%v; casing should be left to citext", got, err)
	}
}
