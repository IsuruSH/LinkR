package domain

import (
	"testing"
	"time"
)

func TestLink_IsExpired(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		expiresAt *time.Time
		want      bool
	}{
		{"never expires", nil, false},
		{"expires in the future", ptr(now.Add(time.Hour)), false},
		{"expired an hour ago", ptr(now.Add(-time.Hour)), true},
		// Boundary: expiry is inclusive — a link is expired the instant it reaches
		// expires_at, not one tick later.
		{"expires exactly now", ptr(now), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := Link{ExpiresAt: tt.expiresAt}
			if got := l.IsExpired(now); got != tt.want {
				t.Errorf("IsExpired = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateExpiry(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		exp     *time.Time
		wantErr bool
	}{
		{"nil is valid (never expires)", nil, false},
		{"one minute out", ptr(now.Add(time.Minute)), false},
		{"at the horizon", ptr(now.Add(MaxExpiryHorizon - time.Hour)), false},
		{"in the past", ptr(now.Add(-time.Second)), true},
		{"exactly now is not the future", ptr(now), true},
		{"beyond the horizon", ptr(now.Add(MaxExpiryHorizon + time.Hour)), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateExpiry(tt.exp, now)
			if tt.wantErr != (err != nil) {
				t.Fatalf("ValidateExpiry(%v) err = %v, wantErr %v", tt.exp, err, tt.wantErr)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
