# Linkr

A URL shortener with click analytics. Go + Postgres + Redis on the back, Next.js on the front.

The interesting parts are the redirect hot path (Redis-fronted, with a Postgres fallback) and the click recorder (bounded channel → worker pool → batched, transactional inserts). Both are explained in **[DECISIONS.md](DECISIONS.md)**, which is where the engineering reasoning lives.

---

## Run it

**Prerequisites:** Docker with Compose v2. Nothing else — no Go, no Node, no `psql`.

```bash
git clone <repo-url> linkr
cd linkr
cp .env.example .env      # every variable is documented inline
docker compose up --build
```

That is the whole thing. On first boot the backend applies its migrations and seeds a demo account, so there is something to look at immediately.

| | |
|---|---|
| **Dashboard** | <http://localhost:3000> |
| **API** | <http://localhost:8080> |
| **Demo login** | `demo@linkr.dev` / `demo-password-123` |

The seed creates five links with 30 days of backdated click history (and one link with zero clicks, so the empty state is visible). Short links resolve at `http://localhost:8080/{code}` — try <http://localhost:8080/gh-repo>, then reload the stats page and watch the count move.

`make up` does the same thing and creates `.env` for you if it is missing.

### Stopping

```bash
docker compose down       # keep the data
docker compose down -v    # drop the volumes too — next `up` re-seeds
```

---

## What's exposed

| Method | Path | Auth | Notes |
|---|---|---|---|
| `POST` | `/api/auth/register` | — | Returns a JWT |
| `POST` | `/api/auth/login` | — | Returns a JWT |
| `POST` | `/api/links` | JWT | Optional `alias`; validates the URL |
| `GET` | `/api/links` | JWT | Keyset pagination: `?limit=&cursor=` |
| `GET` | `/api/links/{code}/stats` | JWT | `?range=7d\|30d\|all` |
| `DELETE` | `/api/links/{code}` | JWT | Invalidates the cache entry |
| `GET` | `/{code}` | **public** | 302. The hot path. |
| `GET` | `/healthz` | — | Liveness. Touches nothing. |
| `GET` | `/readyz` | — | Readiness. Probes Postgres + Redis. |

Failures share one envelope. Successes return the resource directly, unwrapped:

```json
{ "error": { "code": "ALIAS_TAKEN", "message": "that alias is already taken" } }
```

`code` is a stable machine string — switch on it, not on the message.

### Try it from the shell

```bash
TOKEN=$(curl -s -X POST localhost:8080/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"demo@linkr.dev","password":"demo-password-123"}' | jq -r .access_token)

curl -s -X POST localhost:8080/api/links \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"url":"https://go.dev","alias":"godev"}' | jq

curl -si localhost:8080/godev | head -3          # 302 -> https://go.dev
curl -s "localhost:8080/api/links/godev/stats?range=7d" \
  -H "Authorization: Bearer $TOKEN" | jq
```

---

## Tests

```bash
make test              # unit tests, race detector on, no services needed
make test-integration  # against a real Postgres + Redis from compose
make lint              # go vet + gofmt
```

`make test` runs inside `golang:1.25` rather than on your host. The race detector needs cgo and a C toolchain, and a stock Windows box has neither — running it in the image means `go test -race` behaves identically everywhere.

The unit tests cover the things that can actually break: short-code generation (including a chi-square test that fails if the rejection sampling is ever "simplified" back to modulo), URL and alias validation, the click worker's four behaviours (flush-by-size, flush-by-tick, drop-when-full, drain-on-shutdown), cache invalidation, and JWT algorithm pinning.

The integration tests assert what a fake cannot — most importantly that **the redirect does not wait on the click write**. With the flush interval set so nothing can flush, 20 redirects serve in ~6ms with zero rows persisted; after the drain, all 20 are there. If the write ever moved onto the critical path, that test finds rows and fails.

---

## Running without Docker

You still need Postgres 16 and Redis 7 somewhere.

```bash
# backend
cd backend
export DATABASE_URL="postgres://linkr:linkr_dev_password@localhost:5432/linkr?sslmode=disable"
export REDIS_URL="redis://localhost:6379/0"
export JWT_SECRET="a-long-random-string-of-at-least-32-bytes"
go run ./cmd/server          # migrates, seeds, then listens on :8080

# frontend, in another shell
cd frontend
npm install
npm run dev                  # http://localhost:3000
```

The backend refuses to start on a bad config rather than failing on the first request, and it reports *every* missing variable at once instead of one per restart.

---

## Make targets

| | |
|---|---|
| `make up` / `make down` | start / stop the stack (`make down ARGS=-v` drops volumes) |
| `make logs` | tail everything |
| `make migrate` | apply migrations explicitly (they also run on boot) |
| `make seed` | insert the demo data explicitly (idempotent) |
| `make test` / `make test-integration` | see above |
| `make lint` / `make fmt` | `go vet` + `gofmt` |
| `make sqlc` | regenerate `internal/db` from `migrations/` + `queries/` |

---

## Layout

```
backend/
  cmd/server/          all dependency wiring lives in main.go
  migrations/          numbered SQL; embedded in the binary, run on boot
  internal/
    domain/            entities, errors, short codes, validation  (zero deps)
    db/                sqlc-GENERATED — never edited by hand
      queries/         the hand-written SQL it is generated from
    repository/        maps db rows -> domain, pg errors -> domain errors
    cache/             Cache interface + Redis implementation
    worker/            Recorder interface + the click worker pool
    auth/              bcrypt + JWT primitives (no HTTP, no SQL)
    service/           business logic and orchestration
    handler/           HTTP handlers + wire DTOs
    middleware/        request ID, structured logging, recovery, auth
    httpx/             the error envelope and the one status-code mapping
  tests/               integration tests (//go:build integration)

frontend/
  app/                 App Router. Server components by default.
    api/auth/          exchanges a JWT for an httpOnly cookie
    api/bff/           reads that cookie, forwards Bearer to Go
  components/ui/       shadcn, generated
  components/features/ feature-scoped components
  hooks/ lib/ providers/ types/
  proxy.ts             route protection (Next 16's middleware.ts successor)
```

Dependencies point inward: `handler → service → repository → domain`. Handlers never touch SQL; repositories never touch HTTP; `domain` imports nothing but the standard library.

`internal/db` is generated by sqlc and committed. Change a query or a migration, run `make sqlc`, and a mismatch becomes a compile error rather than a runtime one.

---

## Configuration

Everything is in [`.env.example`](.env.example), documented inline. `.env` is gitignored.

The two that matter:

- **`JWT_SECRET`** — must be ≥32 bytes. The server will not start otherwise, and refuses the example value outright in production. A short secret is worse than none: it looks configured and is brute-forceable.
- **`NEXT_PUBLIC_API_URL`** — the address the *browser* uses. Next inlines `NEXT_PUBLIC_*` at build time, so it is a compose build arg as well as a runtime variable. The Next server itself reaches the API at `BACKEND_INTERNAL_URL` (`http://backend:8080`), because inside the container `localhost` is the frontend.

The click worker's knobs (`CLICK_BUFFER_SIZE`, `CLICK_BATCH_SIZE`, `CLICK_FLUSH_INTERVAL`, `CLICK_WORKERS`) and the pgx pool sizing are all tunable from `.env`; the reasoning behind the defaults is in `internal/config/config.go` and DECISIONS.md.

---

## Horizontal scale

The backend keeps no per-process state, so it scales out. Plain `--scale backend=3` on the base file does *not* work, though, and it is worth being precise about why: the base file publishes the backend on a fixed host port, and three containers cannot all bind `:8080`. That is a port-mapping problem, not a statelessness problem.

The overlay drops the published port and puts an nginx L7 balancer in front:

```bash
docker compose -f docker-compose.yml -f docker-compose.scale.yml up --scale backend=3
```

Not a line of application code changes. Everything still answers on <http://localhost:8080>.

Verified: 30 redirects through the balancer land 11 / 11 / 8 across the three replicas, and the click count comes out exactly right, because the counter lives in Postgres and the cache lives in Redis rather than in a per-process map.

Booting three replicas against an **empty** database is the more interesting test — all three race to migrate and seed. Migrations and the seed each take a Postgres advisory lock, so exactly one replica wins and the others find a non-empty table and skip. Without that lock, three replicas would each insert the demo history and `gh-repo` would show 1,782 clicks instead of 594.

What *doesn't* scale today, what breaks first, and where each fix plugs in is in [DECISIONS.md](DECISIONS.md).
