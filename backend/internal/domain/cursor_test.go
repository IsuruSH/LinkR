package domain

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCursor_RoundTrip(t *testing.T) {
	t.Parallel()

	// Microsecond precision: Postgres stores timestamptz to the microsecond, and
	// truncating it here would reintroduce the created_at ties that the ID in the
	// cursor exists to break.
	want := Cursor{
		CreatedAt: time.Date(2026, 7, 10, 11, 22, 33, 456789000, time.UTC),
		ID:        uuid.MustParse("2f1c9d4e-0000-4000-8000-abcdefabcdef"),
	}

	got, err := DecodeCursor(want.Encode())
	if err != nil {
		t.Fatalf("DecodeCursor: %v", err)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v (precision lost in the round trip)", got.CreatedAt, want.CreatedAt)
	}
	if got.ID != want.ID {
		t.Errorf("ID = %v, want %v", got.ID, want.ID)
	}
}

func TestCursor_EncodeIsOpaqueAndURLSafe(t *testing.T) {
	t.Parallel()

	c := Cursor{CreatedAt: time.Now().UTC(), ID: uuid.New()}
	token := c.Encode()

	// Goes in a query string unescaped: no '+', '/', or '=' padding.
	for _, bad := range []rune{'+', '/', '='} {
		for _, r := range token {
			if r == bad {
				t.Errorf("token %q contains %q, which needs URL escaping", token, bad)
			}
		}
	}
}

func TestDecodeCursor_RejectsGarbage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"not base64", "!!!not-base64!!!"},
		{"base64 but no separator", base64.RawURLEncoding.EncodeToString([]byte("nopipe"))},
		{"bad timestamp", base64.RawURLEncoding.EncodeToString([]byte("not-a-time|" + uuid.Nil.String()))},
		{"bad uuid", base64.RawURLEncoding.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano) + "|not-a-uuid"))},
		{"only a timestamp", base64.RawURLEncoding.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeCursor(tt.token)
			if err == nil {
				t.Fatalf("DecodeCursor(%q) = nil error, want ErrInvalidCursor", tt.token)
			}
			// Every failure is the same error: a client probing with malformed
			// cursors learns nothing about the internal format.
			if !errors.Is(err, ErrInvalidCursor) {
				t.Errorf("error = %v, want ErrInvalidCursor", err)
			}
		})
	}
}

func TestClampPageSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   int
		want int32
	}{
		{0, DefaultPageSize},
		{-1, DefaultPageSize},
		{-9999, DefaultPageSize},
		{1, 1},
		{20, 20},
		{MaxPageSize, MaxPageSize},
		{MaxPageSize + 1, MaxPageSize},
		{1_000_000, MaxPageSize},
	}
	for _, tt := range tests {
		if got := ClampPageSize(tt.in); got != tt.want {
			t.Errorf("ClampPageSize(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
