package domain

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// MaxURLLength matches the de-facto browser ceiling. Beyond this a redirect is
// useless anyway, and an unbounded field is an invitation to fill the table.
const MaxURLLength = 2048

// MaxExpiryHorizon caps how far in the future a link may be set to expire. Ten
// years is effectively "never" for a short link, and an unbounded value is more
// likely a client bug (a millisecond timestamp mistaken for seconds) than intent.
const MaxExpiryHorizon = 10 * 365 * 24 * time.Hour

// ValidateExpiry checks a requested expiry against now. A nil expiry is valid and
// means the link never expires. The expiry must be in the future — a link that is
// born expired is a client mistake, not a feature — and within the horizon.
func ValidateExpiry(expiresAt *time.Time, now time.Time) error {
	if expiresAt == nil {
		return nil
	}
	if !expiresAt.After(now) {
		return NewError(CodeValidation, "expiry must be in the future").
			WithDetails(map[string]string{"expires_at": "must be a future time"})
	}
	if expiresAt.After(now.Add(MaxExpiryHorizon)) {
		return NewError(CodeValidation, "expiry is too far in the future").
			WithDetails(map[string]string{"expires_at": "must be within 10 years"})
	}
	return nil
}

var aliasPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{3,32}$`)

// reservedAliases are codes that would shadow a real route. GET /{code} is
// registered at the router root, so without this an alias of "healthz" or "api"
// would be unreachable at best and would shadow the API at worst.
//
// This must stay in sync with the routes registered in main.go. It is a map so
// the lookup is a hash, and a var so tests can assert every top-level route is
// covered.
var reservedAliases = map[string]struct{}{
	"api":         {},
	"healthz":     {},
	"readyz":      {},
	"metrics":     {},
	"login":       {},
	"register":    {},
	"dashboard":   {},
	"admin":       {},
	"static":      {},
	"_next":       {},
	"favicon.ico": {},
	"robots.txt":  {},
}

// ValidateLongURL accepts absolute http(s) URLs only.
//
// Scope note: we never fetch this URL, we only 302 to it. So this is
// open-redirect hygiene and storage hygiene — NOT SSRF defense. Blocking
// 169.254.169.254 here would imply a protection we do not actually provide,
// since the browser, not the server, follows the redirect. If Linkr ever
// previews or unfurls a target, that is when a private-range blocklist becomes
// load-bearing.
func ValidateLongURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)

	if raw == "" {
		return "", NewError(CodeInvalidURL, "url is required").
			WithDetails(map[string]string{"url": "must not be empty"})
	}
	if len(raw) > MaxURLLength {
		return "", NewError(CodeInvalidURL, "url is too long").
			WithDetails(map[string]string{"url": fmt.Sprintf("must be at most %d characters", MaxURLLength)})
	}
	// Control characters (including a bare \n) can smuggle a header into a
	// naive Location write. net/http sanitizes it, but a bad URL should not
	// reach storage in the first place.
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", NewError(CodeInvalidURL, "url contains control characters").
				WithDetails(map[string]string{"url": "must not contain control characters"})
		}
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", NewError(CodeInvalidURL, "url is not parseable").
			WithDetails(map[string]string{"url": "must be a valid absolute URL"})
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", NewError(CodeInvalidURL, "url scheme must be http or https").
			WithDetails(map[string]string{"url": fmt.Sprintf("scheme %q is not allowed", u.Scheme)})
	}
	// url.Parse("https://") succeeds with an empty Host, as does "http:///path".
	if u.Host == "" {
		return "", NewError(CodeInvalidURL, "url is missing a host").
			WithDetails(map[string]string{"url": "must include a host, e.g. https://example.com"})
	}

	return u.String(), nil
}

// ValidateAlias checks a user-supplied custom code. The same pattern is enforced
// by a CHECK constraint in the database: this returns a friendly error, the
// constraint guarantees the invariant even if a future caller skips this.
func ValidateAlias(alias string) error {
	if !aliasPattern.MatchString(alias) {
		return NewError(CodeInvalidAlias, "alias must be 3-32 characters of letters, digits, hyphen or underscore").
			WithDetails(map[string]string{"alias": "allowed: A-Z a-z 0-9 _ -, length 3-32"})
	}
	// Case-insensitive: short_code lookup is exact, but reserving "API" while
	// allowing "api" would be a route-shadowing hole with extra steps.
	if _, reserved := reservedAliases[strings.ToLower(alias)]; reserved {
		return NewError(CodeReservedAlias, fmt.Sprintf("alias %q is reserved", alias)).
			WithDetails(map[string]string{"alias": "this name is used by the application"})
	}
	return nil
}

// IsReservedAlias is exported so main.go's route table can be asserted against
// it in a test, rather than the two drifting apart silently.
func IsReservedAlias(alias string) bool {
	_, ok := reservedAliases[strings.ToLower(alias)]
	return ok
}
