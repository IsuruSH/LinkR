package domain

import (
	"math"
	"strings"
	"testing"
)

func TestGenerateShortCode_ShapeAndAlphabet(t *testing.T) {
	for i := 0; i < 1000; i++ {
		code, err := GenerateShortCode()
		if err != nil {
			t.Fatalf("GenerateShortCode: %v", err)
		}
		if len(code) != ShortCodeLength {
			t.Fatalf("len(%q) = %d, want %d", code, len(code), ShortCodeLength)
		}
		for _, r := range code {
			if !strings.ContainsRune(alphabet, r) {
				t.Fatalf("code %q contains %q, which is outside the base62 alphabet", code, r)
			}
		}
		// A generated code must satisfy the same rules the database enforces,
		// or CreateLink would fail its CHECK constraint at runtime.
		if err := ValidateAlias(code); err != nil {
			t.Fatalf("generated code %q fails ValidateAlias: %v", code, err)
		}
	}
}

func TestGenerateShortCode_NoCollisionsInSmallSample(t *testing.T) {
	const n = 20_000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		code, err := GenerateShortCode()
		if err != nil {
			t.Fatalf("GenerateShortCode: %v", err)
		}
		if _, dup := seen[code]; dup {
			// Over 62^7, P(any collision in 20k draws) ≈ 1e-3... actually ~5.7e-4.
			// Seeing one means the generator is not sampling the space it claims.
			t.Fatalf("duplicate code %q after %d draws", code, i)
		}
		seen[code] = struct{}{}
	}
}

// This is the test that would fail if someone "simplified" the generator back to
// `alphabet[b % 62]` over raw bytes. Modulo folding maps 0..255 onto 62 symbols
// unevenly: the first 8 symbols get 5 source bytes each, the rest get 4, making
// them ~25% more likely. Rejection sampling keeps the distribution flat.
func TestGenerateShortCode_DistributionIsUnbiased(t *testing.T) {
	const draws = 60_000
	counts := make(map[rune]int, len(alphabet))

	for i := 0; i < draws; i++ {
		code, err := GenerateShortCode()
		if err != nil {
			t.Fatalf("GenerateShortCode: %v", err)
		}
		for _, r := range code {
			counts[r]++
		}
	}

	if len(counts) != len(alphabet) {
		t.Fatalf("only %d of %d alphabet symbols ever appeared", len(counts), len(alphabet))
	}

	total := draws * ShortCodeLength
	expected := float64(total) / float64(len(alphabet))

	// Chi-square goodness of fit. df = 61; the 99.9th percentile is ≈ 112.
	// A modulo-biased generator lands in the thousands, so this separates the
	// two decisively while effectively never failing on a fair one.
	var chiSquare float64
	for _, r := range alphabet {
		diff := float64(counts[r]) - expected
		chiSquare += (diff * diff) / expected
	}

	const criticalValue = 112.0
	if chiSquare > criticalValue {
		t.Errorf("chi-square = %.1f exceeds %.1f (df=61, p=0.001): distribution is biased, "+
			"which is what happens when rejection sampling is replaced by modulo",
			chiSquare, criticalValue)
	}
	if math.IsNaN(chiSquare) {
		t.Fatal("chi-square is NaN")
	}
}

func TestValidateLongURL(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr Code
	}{
		{"plain https", "https://example.com", ""},
		{"http with path and query", "http://example.com/a/b?c=d#e", ""},
		{"surrounding whitespace is trimmed", "  https://example.com  ", ""},
		{"port and userinfo", "https://user:pw@example.com:8443/x", ""},
		{"unicode host", "https://例え.jp/path", ""},

		{"empty", "", CodeInvalidURL},
		{"whitespace only", "   ", CodeInvalidURL},
		{"no scheme", "example.com", CodeInvalidURL},
		{"relative", "/just/a/path", CodeInvalidURL},
		{"javascript scheme", "javascript:alert(1)", CodeInvalidURL},
		{"data scheme", "data:text/html;base64,PHNjcmlwdD4=", CodeInvalidURL},
		{"ftp scheme", "ftp://example.com/f", CodeInvalidURL},
		{"file scheme", "file:///etc/passwd", CodeInvalidURL},
		{"scheme but no host", "https://", CodeInvalidURL},
		{"empty authority", "http:///path", CodeInvalidURL},
		{"newline injection", "https://example.com/\nLocation: https://evil.tld", CodeInvalidURL},
		{"carriage return", "https://example.com/\r\nSet-Cookie: a=b", CodeInvalidURL},
		{"null byte", "https://example.com/\x00", CodeInvalidURL},
		{"too long", "https://example.com/" + strings.Repeat("a", MaxURLLength), CodeInvalidURL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateLongURL(tt.in)

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateLongURL(%q) = error %v, want success", tt.in, err)
				}
				if got == "" {
					t.Error("returned an empty normalized URL")
				}
				return
			}

			if err == nil {
				t.Fatalf("ValidateLongURL(%q) = %q, want error %s", tt.in, got, tt.wantErr)
			}
			var derr *Error
			if !asDomainError(err, &derr) || derr.Code != tt.wantErr {
				t.Fatalf("error = %v, want code %s", err, tt.wantErr)
			}
		})
	}
}

func TestValidateAlias(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr Code
	}{
		{"simple", "my-link", ""},
		{"underscores and digits", "go_2_docs", ""},
		{"minimum length", "abc", ""},
		{"maximum length", strings.Repeat("a", 32), ""},

		{"too short", "ab", CodeInvalidAlias},
		{"too long", strings.Repeat("a", 33), CodeInvalidAlias},
		{"space", "my link", CodeInvalidAlias},
		{"slash would break routing", "a/b", CodeInvalidAlias},
		{"dot", "a.b", CodeInvalidAlias},
		{"unicode", "café", CodeInvalidAlias},
		{"empty", "", CodeInvalidAlias},

		{"reserved: api", "api", CodeReservedAlias},
		{"reserved: healthz", "healthz", CodeReservedAlias},
		{"reserved is case-insensitive", "ApI", CodeReservedAlias},
		{"reserved: dashboard", "dashboard", CodeReservedAlias},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAlias(tt.in)

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateAlias(%q) = %v, want nil", tt.in, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateAlias(%q) = nil, want %s", tt.in, tt.wantErr)
			}
			var derr *Error
			if !asDomainError(err, &derr) || derr.Code != tt.wantErr {
				t.Fatalf("error = %v, want code %s", err, tt.wantErr)
			}
		})
	}
}

// Every alias short enough to be reserved must actually be rejected. This
// guards the invariant that a custom alias can never shadow a real route.
func TestReservedAliases_AreRejectedOrUnreachable(t *testing.T) {
	for name := range reservedAliases {
		if !IsReservedAlias(name) {
			t.Errorf("IsReservedAlias(%q) = false", name)
		}
		// Names like "favicon.ico" fail the pattern first, which is fine: they
		// are unreachable as aliases either way. What must never happen is a
		// reserved name passing ValidateAlias.
		if err := ValidateAlias(name); err == nil {
			t.Errorf("ValidateAlias(%q) = nil; a reserved route name was accepted as an alias", name)
		}
	}
}

func asDomainError(err error, target **Error) bool {
	de, ok := err.(*Error)
	if ok {
		*target = de
	}
	return ok
}
