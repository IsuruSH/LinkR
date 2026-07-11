package handler

import (
	"time"

	"github.com/IsuruSh/linkr/internal/domain"
)

// DTOs live here rather than in domain so the wire format can change without
// touching business logic — and so no domain struct accidentally grows a json
// tag that leaks a password hash.

type createLinkRequest struct {
	URL   string `json:"url"`
	Alias string `json:"alias,omitempty"`
	// ExpiresAt is an optional RFC3339 instant. Omitted or null means the link
	// never expires. A pointer so "absent" and "the zero time" are distinct.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// linkResponse is the shape of a link on the wire.
//
// ShortURL is rendered server-side from PUBLIC_BASE_URL rather than assembled by
// the client, so a deployment behind a custom domain does not need a frontend
// rebuild to produce correct links.
type linkResponse struct {
	ShortCode  string     `json:"short_code"`
	ShortURL   string     `json:"short_url"`
	LongURL    string     `json:"long_url"`
	ClickCount int64      `json:"click_count"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

func toLinkResponse(l domain.Link, baseURL string) linkResponse {
	return linkResponse{
		ShortCode:  l.ShortCode,
		ShortURL:   baseURL + "/" + l.ShortCode,
		LongURL:    l.LongURL,
		ClickCount: l.ClickCount,
		CreatedAt:  l.CreatedAt,
		ExpiresAt:  l.ExpiresAt,
	}
}

// listLinksResponse carries the keyset cursor.
//
// next_cursor is null on the last page. There is no `total`: counting every
// matching row on every page is the cost keyset pagination exists to avoid, and
// the dashboard does not display one.
type listLinksResponse struct {
	Items      []linkResponse `json:"items"`
	NextCursor *string        `json:"next_cursor"`
}

type dailyClicksResponse struct {
	// Date, not timestamp: these are UTC day buckets, and sending an ISO instant
	// invites the frontend to shift them into local time and mislabel every bar.
	Day    string `json:"day"`
	Clicks int64  `json:"clicks"`
}

type statsResponse struct {
	ShortCode string `json:"short_code"`
	ShortURL  string `json:"short_url"`
	LongURL   string `json:"long_url"`
	// TotalClicks is lifetime, from the denormalized counter. It is deliberately
	// not the sum of Series, which is windowed by ?range=.
	TotalClicks int64                 `json:"total_clicks"`
	Range       string                `json:"range"`
	Series      []dailyClicksResponse `json:"series"`
}

func toStatsResponse(s domain.LinkStats, rangeName, baseURL string) statsResponse {
	series := make([]dailyClicksResponse, len(s.Series))
	for i, d := range s.Series {
		series[i] = dailyClicksResponse{
			Day:    d.Day.Format(time.DateOnly),
			Clicks: d.Clicks,
		}
	}
	return statsResponse{
		ShortCode:   s.Link.ShortCode,
		ShortURL:    baseURL + "/" + s.Link.ShortCode,
		LongURL:     s.Link.LongURL,
		TotalClicks: s.TotalClicks,
		Range:       rangeName,
		Series:      series,
	}
}

type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// tokenResponse never includes the user's password hash, and the struct it is
// built from is constructed field by field rather than embedding domain.User.
type tokenResponse struct {
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	ExpiresAt   time.Time `json:"expires_at"`
	User        userDTO   `json:"user"`
}

type userDTO struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}
