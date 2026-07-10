package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/IsuruSh/linkr/internal/domain"
)

const testSecret = "test-secret-that-is-at-least-32-bytes-long"

func TestHashPassword_VerifyRoundTrip(t *testing.T) {
	t.Parallel()

	const pw = "correct horse battery staple"

	hash, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == pw {
		t.Fatal("hash equals the plaintext")
	}
	if !strings.HasPrefix(hash, "$2a$") && !strings.HasPrefix(hash, "$2b$") {
		t.Errorf("hash %q is not a bcrypt hash", hash)
	}
	if !VerifyPassword(hash, pw) {
		t.Error("VerifyPassword rejected the correct password")
	}
	if VerifyPassword(hash, pw+"x") {
		t.Error("VerifyPassword accepted a wrong password")
	}
}

// The same password must not produce the same hash twice: bcrypt salts each
// call. Identical hashes across users would let one rainbow table serve all.
func TestHashPassword_IsSalted(t *testing.T) {
	t.Parallel()

	a, err := HashPassword("same-password-here")
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassword("same-password-here")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two hashes of the same password are identical; the salt is missing")
	}
	if !VerifyPassword(a, "same-password-here") || !VerifyPassword(b, "same-password-here") {
		t.Error("both salted hashes must still verify")
	}
}

// bcrypt truncates at 72 bytes. If we accepted longer input, this 80-char
// password and its 72-char prefix would be the same credential.
func TestHashPassword_RejectsOverLongPassword(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 80)
	if _, err := HashPassword(long); err == nil {
		t.Fatal("HashPassword accepted an 80-byte password; bcrypt would silently truncate it at 72")
	}

	// Prove the hazard is real: hash the truncated form directly and show the
	// long password verifies against it.
	truncated := strings.Repeat("a", 72)
	hash, err := HashPassword(truncated)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, long) {
		t.Skip("bcrypt implementation no longer truncates; the guard is now belt-and-braces")
	}
	t.Log("confirmed: an 80-byte password verifies against the hash of its 72-byte prefix")
}

func TestValidatePassword(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"minimum", strings.Repeat("a", 8), false},
		{"typical", "hunter2hunter2", false},
		{"maximum", strings.Repeat("a", 72), false},
		{"too short", "short", true},
		{"empty", "", true},
		{"too long", strings.Repeat("a", 73), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePassword(tt.in)
			if tt.wantErr != (err != nil) {
				t.Fatalf("ValidatePassword(len=%d) error = %v, wantErr %v", len(tt.in), err, tt.wantErr)
			}
		})
	}
}

func TestIssuer_IssueVerifyRoundTrip(t *testing.T) {
	t.Parallel()

	iss := NewIssuer(testSecret, time.Hour)
	userID := uuid.New()
	now := time.Now()

	token, expiresAt, err := iss.Issue(userID, now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !expiresAt.After(now) {
		t.Error("expiresAt is not in the future")
	}

	got, err := iss.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got != userID {
		t.Errorf("subject = %v, want %v", got, userID)
	}
}

func TestIssuer_RejectsExpiredToken(t *testing.T) {
	t.Parallel()

	iss := NewIssuer(testSecret, time.Hour)
	// Issued two hours ago with a one-hour TTL.
	token, _, err := iss.Issue(uuid.New(), time.Now().Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := iss.Verify(token); err == nil {
		t.Fatal("Verify accepted an expired token")
	}
}

func TestIssuer_RejectsTokenSignedWithAnotherSecret(t *testing.T) {
	t.Parallel()

	attacker := NewIssuer("a-completely-different-secret-32-bytes!!", time.Hour)
	token, _, err := attacker.Issue(uuid.New(), time.Now())
	if err != nil {
		t.Fatal(err)
	}

	victim := NewIssuer(testSecret, time.Hour)
	if _, err := victim.Verify(token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("Verify error = %v, want ErrUnauthorized", err)
	}
}

// The algorithm-confusion attack. An unsigned token claiming alg=none must be
// rejected because Verify pins the accepted methods. Without
// jwt.WithValidMethods this test fails and anyone can mint any identity.
func TestIssuer_RejectsAlgNone(t *testing.T) {
	t.Parallel()

	forged := jwt.NewWithClaims(jwt.SigningMethodNone, Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   uuid.New().String(),
			Issuer:    "linkr",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	tokenString, err := forged.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("constructing the forged token: %v", err)
	}

	iss := NewIssuer(testSecret, time.Hour)
	if _, err := iss.Verify(tokenString); err == nil {
		t.Fatal("Verify accepted an alg=none token; the signing method is not pinned")
	}
}

// A token with no expiry claim must be refused: it would never age out.
func TestIssuer_RejectsTokenWithoutExpiry(t *testing.T) {
	t.Parallel()

	forged := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: uuid.New().String(),
			Issuer:  "linkr",
		},
	})
	tokenString, err := forged.SignedString([]byte(testSecret))
	if err != nil {
		t.Fatal(err)
	}

	iss := NewIssuer(testSecret, time.Hour)
	if _, err := iss.Verify(tokenString); err == nil {
		t.Fatal("Verify accepted a token with no exp claim")
	}
}

func TestIssuer_RejectsWrongIssuer(t *testing.T) {
	t.Parallel()

	forged := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   uuid.New().String(),
			Issuer:    "someone-else",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	tokenString, err := forged.SignedString([]byte(testSecret))
	if err != nil {
		t.Fatal(err)
	}

	iss := NewIssuer(testSecret, time.Hour)
	if _, err := iss.Verify(tokenString); err == nil {
		t.Fatal("Verify accepted a token from a different issuer")
	}
}

func TestIssuer_RejectsGarbage(t *testing.T) {
	t.Parallel()

	iss := NewIssuer(testSecret, time.Hour)
	for _, bad := range []string{"", "not.a.token", "a.b.c", "Bearer x"} {
		if _, err := iss.Verify(bad); err == nil {
			t.Errorf("Verify(%q) = nil error", bad)
		}
	}
}
