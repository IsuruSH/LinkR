# Milestone 2 — Plan

Milestone 1 shipped the spec-complete core and a clean deployment story. M2 adds
the three things you selected: **rate limiting**, **link expiration**, and a
**CI workflow**. Each is additive against M1's structure — no refactor, which is
the property M1 was built to have.

Scope notes:

- **Dropped from the original M2 sketch:** Prometheus `/metrics` (you didn't
  select it). The worker's `clicks_dropped_total` / `clicks_lost_total` stay
  private atomics behind `worker.Stats()`; DECISIONS.md keeps `/metrics` as
  future work with `internal/metrics/` named as the insertion point.
- **Already done in M1, not re-planned:** stats time-range tabs (7d/30d/all),
  dark-mode toggle, and deployment.

Build order: **1 → 2 → 3.** Rate limiting and expiration are independent; CI goes
last so it gates the finished set. No commits (per standing instruction) — each
slice is built and verified for review.

---

## Slice 1 — Rate limiting on `POST /api/links`

A per-user fixed-window limit, enforced in Redis so it holds across
`--scale backend=N` — an in-process counter would let each replica grant the
full quota.

### What gets built

- **`internal/cache/ratelimit.go`** — a `RateLimiter` on the existing
  `RedisCache`. One method:

  ```go
  // Allow reports whether this key is under its limit for the current window,
  // and how long until the window resets (for Retry-After).
  Allow(ctx, key string, limit int, window time.Duration) (allowed bool, retryAfter time.Duration, err error)
  ```

  Implemented with a **Lua script** (`EVAL`), not `INCR` then `EXPIRE` as two
  calls. Two calls race: a crash between them leaves a key with no TTL, and that
  user is locked out forever. The script does `INCR`, sets `EXPIRE` only on the
  first hit, and returns the count and remaining TTL — atomically, one round trip.

- **`internal/middleware/ratelimit.go`** — `RateLimit(limiter, cfg)` middleware.
  Keys on the authenticated user ID (`middleware.UserIDFrom`), so it mounts
  *inside* the protected group, after `RequireAuth`. Key shape:
  `ratelimit:create:v1:{userID}:{unix-window}`.

- Applied to **`POST /api/links` only**, in `link.go`'s `Routes()`. Not a global
  middleware — the redirect and the reads are not rate-limited.

- **Domain + wire:** new `domain.CodeRateLimited` → `429` in `httpx`'s status
  map. The handler sets `Retry-After` from the returned duration. The frontend's
  `ApiErrorCode` union and `create-link` mutation surface it as a toast
  ("Slow down — try again in Ns"), not a form-field error.

### Config

`.env.example` gains (they are not there today):

```
RATE_LIMIT_ENABLED=true
RATE_LIMIT_PER_MINUTE=30     # POST /api/links, per user
```

`config.Load` parses both. When disabled, the middleware is simply not mounted —
no per-request branch on a boolean.

### Fail-open, and the trade to document

If Redis is **down**, the limiter logs and **allows** the request. Rate limiting
is abuse control, not correctness; taking creation offline because the cache
blinked would be a worse outage than the abuse it prevents. (This is the mirror
of the redirect's fail-*to-Postgres* posture — both degrade toward availability.)

**DECISIONS.md entry:** fixed window permits up to 2× the limit across a window
boundary (30 at 11:59:59, 30 more at 12:00:00). A sliding-window-log or a token
bucket (also one Lua script) closes it. Fixed window is chosen because the burst
is bounded, harmless for link creation, and the simplest thing that is honest
about its own edge — which is more defensible than a token bucket whose three
knobs invite misconfiguration.

### Tests

- Unit, against a fake limiter: middleware returns 429 + `Retry-After` at limit+1;
  passes below; keys are per-user (user A's spending does not limit user B).
- Unit, the Lua logic via a `miniredis` or the real Redis in integration: the
  window actually resets, and a first hit always gets a TTL.
- Integration: 30 creates succeed, the 31st is 429, and after the window a create
  succeeds again.
- **The failure the design turns on:** `Allow` with Redis unreachable returns
  `allowed=true` — a test that points the limiter at a dead address and asserts
  creation still works.

---

## Slice 2 — Link expiration

### Migration `0002`

```sql
-- +goose Up
ALTER TABLE links ADD COLUMN expires_at timestamptz;
-- Partial: only links that expire are indexed, so the index stays small even
-- though most links never expire. Serves a future sweep job (see below).
CREATE INDEX links_expires_at_idx ON links (expires_at) WHERE expires_at IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS links_expires_at_idx;
ALTER TABLE links DROP COLUMN expires_at;
```

sqlc regenerates; `expires_at` becomes `*time.Time` (nullable) on `db.Link` and
flows into `domain.Link`. `CreateLink` gains an `expires_at` parameter.

### Redirect behaviour

- An expired link returns **`410 Gone`**, not `404`. The distinction is real: the
  code *was* valid, so 410 ("it existed and is deliberately gone") is more honest
  than 404 ("no such thing") and tells a crawler to stop retrying.
- New `domain.CodeLinkExpired` → `410` in `httpx`.
- The expiry check lives in the **service**, on the resolved entry — not in SQL —
  so it applies equally to a cache hit and a Postgres read.

### The cache interaction — the part worth writing up

The cached entry must not outlive the link. So on cache-fill:

```
ttl := cfg.Cache.TTL
if link.ExpiresAt != nil {
    ttl = min(ttl, time.Until(*link.ExpiresAt))
}
if ttl <= 0 { /* already expired: negative-cache it, serve 410 */ }
```

Without the clamp, a link expiring in 5 minutes with a 24h cache TTL keeps
redirecting for 24h after its death. The `Entry` also needs to carry `ExpiresAt`
so a cache **hit** can enforce expiry without a Postgres read — same reasoning as
why the entry carries the link ID today. Cache key version bumps `v1 → v2`
because `Entry`'s shape changes; old values expire out naturally.

### API + frontend

- `POST /api/links` accepts optional `expires_at` (RFC3339). Validated: must be
  in the future, and (sanity bound) within ~10 years.
- `createLinkSchema` (zod) gains an optional datetime; the create dialog gets a
  native `datetime-local` field, "Expires (optional)".
- The links table shows an "Expires" cell / an "Expired" badge on dead links; the
  stats page still resolves them (owner view is not gated on expiry).

### Not built, but noted

Expired rows are **not swept**. They just stop resolving. A periodic
`DELETE FROM links WHERE expires_at < now()` job (the partial index above exists
for exactly this) is the cleanup, and it is where "one-time links"
(`max_uses`, decrement-and-expire) would also live. DECISIONS.md notes both as
future work; the column and index are the insertion point.

### Tests

- `domain` / `service`: an entry past `expires_at` yields `ErrLinkExpired`; one
  before does not; a nil `expires_at` never expires.
- Cache TTL clamp: unit test that `SetLink`'s TTL is `min(cacheTTL, untilExpiry)`,
  and that an already-expired link is negative-cached rather than served.
- Integration: create with `expires_at` 1s out → resolves now (302), returns 410
  after it passes, and Redis no longer holds it as a positive entry.
- Validation: past `expires_at` on create → `400`.

---

## Slice 3 — CI workflow

A single `.github/workflows/ci.yml`, three jobs, running on push and PR.

### `backend`
```
- go vet ./...
- golangci-lint run
- go test -race ./...                     # unit, no services
- go test -race -tags=integration ./tests/...   # with a Postgres + Redis service container
- go build ./cmd/server                   # the build the image runs
```
Postgres and Redis come from GitHub Actions **service containers**, wired with
the same `DATABASE_URL` / `REDIS_URL` the compose stack uses. This is what makes
the "`go test -race` passes" claim in the README checkable rather than trusted.

### `frontend`
```
- npm ci
- npm run lint
- npx tsc --noEmit
- npm run build            # catches the App Router / RSC errors tsc misses
```

### `docker`
```
- docker build ./backend
- docker build ./frontend
```
Proves both images still build from a clean context — the thing most likely to
rot silently.

### Guard rails
- `sqlc` drift check: regenerate and `git diff --exit-code internal/db/`. A
  migration edited without `make sqlc` fails CI, which is the whole point of
  committing generated code.
- Go module cache and npm cache keyed on the lockfiles, so a no-op run is fast.

No deploy step — Render and Vercel deploy from their own git integration on merge,
and putting deploy creds in Actions would duplicate that with more surface.

---

## Cross-cutting

- **New domain error codes:** `CodeRateLimited` (429), `CodeLinkExpired` (410).
  Both added to `httpx`'s status map and the frontend `ApiErrorCode` union in one
  place each — the existing pattern.
- **DECISIONS.md** gets three short additions: the fixed-window burst trade, the
  expiry/cache-TTL clamp, and moving CI from "future work" to "done". The
  `/metrics`, rollup-table, Redis-Streams, and read-replica entries stay as
  future work with their insertion points.
- **Deployment config**: rate-limit env vars added for the hosted stack.
  API service's env so the hosted demo enforces limits too.

## Milestone exit bar

- `make test` and `make test-integration` green, `-race` clean.
- CI green on a pushed branch (the real proof the workflow works).
- `docker compose down -v && up --build`: create a link that expires in 1 minute,
  watch it 302 then 410; hammer create past the limit, watch the 429 +
  `Retry-After`; confirm both surface correctly in the dashboard.
- `git diff --exit-code internal/db` clean after `make sqlc`.
