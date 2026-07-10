package domain

import (
	"encoding/base64"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Cursor is the keyset position of the last row of a page: the exact tuple the
// next query compares against with `(created_at, id) < (cursor.CreatedAt, cursor.ID)`.
//
// Why the ID is part of it: created_at alone is not unique. Two links created in
// the same microsecond would make a created_at-only cursor either skip a row or
// return it twice. The (timestamp, uuid) pair is total.
type Cursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// Encode renders a cursor as an opaque base64 token.
//
// Opaque is the point: clients must not construct or arithmetic on it. Encoding
// the timestamp as RFC3339Nano keeps the microsecond precision Postgres stores,
// which a Unix-seconds cursor would silently truncate — reintroducing exactly
// the tie the ID is there to break.
func (c Cursor) Encode() string {
	raw := c.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses a token produced by Encode. Every failure is the same
// domain error: a client that hand-crafts a cursor learns nothing about the
// internal format from the response.
func DecodeCursor(token string) (Cursor, error) {
	if token == "" {
		return Cursor{}, ErrInvalidCursor
	}

	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}

	ts, idPart, found := strings.Cut(string(raw), "|")
	if !found {
		return Cursor{}, ErrInvalidCursor
	}

	createdAt, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	id, err := uuid.Parse(idPart)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}

	return Cursor{CreatedAt: createdAt, ID: id}, nil
}

// Pagination bounds. A caller asking for 10,000 rows is either confused or
// hostile; either way the answer is the same.
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// ClampPageSize keeps a client-supplied limit inside sane bounds instead of
// rejecting it, because a too-large limit is not worth a 400.
func ClampPageSize(n int) int32 {
	switch {
	case n <= 0:
		return DefaultPageSize
	case n > MaxPageSize:
		return MaxPageSize
	default:
		return int32(n)
	}
}
