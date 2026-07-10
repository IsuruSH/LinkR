package domain

import (
	"crypto/rand"
	"fmt"
)

const (
	// alphabet is base62. No ambiguity stripping (0/O, 1/l): codes are copied,
	// not read aloud, and shrinking the alphabet costs entropy for no gain here.
	alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

	// ShortCodeLength of 7 gives 62^7 ≈ 3.5e12 codes. At a million links the
	// birthday-collision probability is on the order of 1e-7 per insert, which
	// the unique index catches anyway. Longer codes buy nothing; shorter ones
	// make enumeration cheap.
	ShortCodeLength = 7

	// maxUnbiased is the largest multiple of len(alphabet) that fits in a byte:
	// 62 * 4 = 248. Bytes >= 248 are rejected rather than folded, because
	// b % 62 over the full 0..255 range maps 0..193 to four values and 194..255
	// to three, making the first 8 letters of the alphabet ~33% more likely.
	maxUnbiased = 248
)

// GenerateShortCode returns a cryptographically random base62 code.
//
// Two decisions here that the obvious implementation gets wrong:
//
//  1. crypto/rand, not math/rand. Codes are unguessable by design: a predictable
//     generator lets anyone enumerate every link created after one they own.
//     math/rand is deterministic given the seed, and seeding it from the clock
//     does not fix that.
//
//  2. Rejection sampling, not modulo. See maxUnbiased above. The bias is small
//     but it is free to avoid, and it compounds: it shrinks the effective
//     keyspace and skews collisions toward a subset of codes.
//
// Codes are random rather than a base62-encoded sequence. A sequence needs
// coordination across replicas (or a per-node offset scheme) and produces
// codes that are trivially enumerable — /1, /2, /3 walks every link in the
// system. Random codes need neither, and uniqueness is enforced by the index.
func GenerateShortCode() (string, error) {
	out := make([]byte, 0, ShortCodeLength)

	// Over-read: on average ~3% of bytes are rejected, so one extra read is
	// almost always enough, and the loop handles the rare case where it is not.
	buf := make([]byte, ShortCodeLength+ShortCodeLength/2+1)

	for len(out) < ShortCodeLength {
		if _, err := rand.Read(buf); err != nil {
			return "", WrapError(err, CodeCodeGeneration, "reading random bytes")
		}
		for _, b := range buf {
			if b >= maxUnbiased {
				continue // reject rather than fold, to keep the distribution flat
			}
			out = append(out, alphabet[b%byte(len(alphabet))])
			if len(out) == ShortCodeLength {
				break
			}
		}
	}
	return string(out), nil
}

// MaxCodeGenerationAttempts bounds the retry loop when a generated code loses a
// race against the unique index. Three attempts against a 3.5e12 keyspace means
// giving up is a signal that something else is wrong — a stuck RNG, or a table
// so full that the scheme itself needs revisiting.
const MaxCodeGenerationAttempts = 3

// ErrCodeGenerationExhausted is returned when every attempt collided.
var ErrCodeGenerationExhausted = NewError(
	CodeCodeGeneration,
	fmt.Sprintf("could not generate a unique short code in %d attempts", MaxCodeGenerationAttempts),
)
