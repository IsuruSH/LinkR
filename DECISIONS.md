# Decisions

A short write-up of the judgment behind Linkr — the choices that mattered and
what they cost.

## 1. The most significant design decisions

**Layered architecture in Go.** `handler → service → repository → db`, with a
`domain` package at the center holding the entities and errors and depending on
nothing. Handlers do HTTP, services do business rules, repositories map database
rows to domain types and Postgres errors (e.g. a `23505` unique violation) to
domain errors (`ErrAliasTaken`). The trade-off is more files and a little mapping
boilerplate versus letting a handler run SQL directly. What it buys: every layer
is testable in isolation — services are tested against a fake repository, so
~90 unit tests run with no database — and generated SQL types never leak into
business logic.

**Redis for the redirect cache, not an in-process map.** The stretch goal asked
for an in-memory cache "with correct invalidation." The moment the backend is
stateless and runs as several replicas, a per-process map *can't* be invalidated
correctly: deleting a link on replica A leaves it cached on replica B. Redis is
one shared cache that every replica reads and evicts. The cost is a network hop
per lookup and one more dependency; the win is correctness across replicas. The
cache stores `{id, url}` so the redirect has the `link_id` to record the click
without a second database read, uses a 24h TTL, and negative-caches misses for
60s so 404-enumeration scans hit Redis instead of Postgres.

**A denormalized `click_count` on `links`.** The dashboard lists N links each
needing a total; `COUNT(*)` per row would be N index scans per page and grows
without bound. Instead the click worker increments a counter in the *same
transaction* that writes the click rows, so the counter is exactly as durable as
the rows. The trade-off is a second write target and the rule that every writer
must go through the worker.

Three smaller decisions worth naming:

- **Auth is a BFF, not a token in the browser.** The Go API returns a JWT; a
  Next.js route handler stores it in an **httpOnly cookie** the browser can't read,
  and dashboard calls go through `/api/bff/*`, which attaches the token
  server-side. Costs one hop; an XSS can never steal a session it can't see.
- **Short codes are `crypto/rand` + rejection sampling**, not `math/rand` and not
  a sequence. Random so codes aren't enumerable (`/1`, `/2`, … would walk every
  link) and need no cross-replica coordination; rejection sampling instead of
  `% 62` so the alphabet stays unbiased. Uniqueness is enforced by the index.
- **Keyset pagination, not offset.** Offset skips or repeats rows when links are
  created mid-scan — which the dashboard does. The cursor `(created_at, id)`
  matches the index exactly, so it's correct *and* constant-cost per page.

## 2. Designing for heavy traffic and concurrent load

**What holds up.** The redirect — the hot path — is served from Redis, touching
Postgres only on a cache miss. Clicks are written asynchronously and in batches
(next section), so the write path can never slow the reads. The backend is
**stateless**, so it scales horizontally: **Nginx round-robins across the backend
replicas** (`BACKEND_REPLICAS`, run at 3 in production), resolving their
container IPs through Docker DNS on a dynamic upstream so new replicas join the
rotation automatically. Each replica keeps a bounded pgx connection pool (20),
which is why three replicas fit comfortably under Postgres's default 100
connections. Listing uses **keyset pagination** on a matching composite index, so
a page costs the same whether it's page 1 or page 1,000. If Redis goes down the
service **degrades** to Postgres-only reads rather than failing, and `/readyz`
reports it.

**What breaks first.** The raw `clicks` table. The stats query groups every click
in the range by day, and the table grows unbounded — at high volume that scan and
those inserts are the first thing to hurt.

**How I'd scale further.** A `click_daily_rollup` table maintained by the same
worker (stats read the rollup, not raw rows), with `clicks` partitioned by month
and aged out; move the in-process channel to Redis Streams or NATS for durability
and multiple consumers; add a Postgres read replica for stats (sqlc's `DBTX`
interface makes that a constructor swap, not a rewrite).

## 3. Async click recording — and what happens under load or on crash

The redirect handler resolves the code, writes the `302`, and *then* hands the
click to a `Recorder`: a **non-blocking send onto a bounded buffered channel**
(capacity 10,000). A pool of 4 worker goroutines drains it, each building a batch
and flushing on whichever comes first — **100 events or a 500ms tick**. A flush is
**one Postgres transaction** that bulk-inserts the click rows (pgx `CopyFrom`) and
increments `click_count` together, so rows and counter commit or fail as a unit.

**Under load:** if the buffer is full — meaning the database is already behind —
`Record` **drops the event and counts it** instead of blocking. Blocking the
redirect would turn a write backlog into a user-facing outage; clicks are
analytics, not billing, so shedding them is the correct degradation, and the
`clicks_dropped_total` counter makes it visible. The "buffer full" warning is
rate-limited to once a second so an overload doesn't also become a log flood.

**On crash:** whatever sits in the channel plus any in-flight batches is lost —
bounded at ≤10,000 events and ≤500ms of accumulation. On a **graceful** shutdown
nothing is lost within the drain budget: `srv.Shutdown` stops accepting requests
first, *then* the channel is closed (safe precisely because no handler can still
send), and the workers flush what they hold, bounded by a 5s timeout.

**Do we lose clicks? Is that acceptable?** Under extreme overload or a hard crash,
yes — a bounded amount. For click analytics that's the right trade: an approximate
count that stays fast beats an exact count that stalls every redirect. If clicks
were billable the answer changes — the fix is Redis Streams (`XADD` on the hot
path, a consumer group and `XACK` in the worker) for at-least-once delivery. The
`Recorder` interface is the seam, so that swap touches only the worker package.

## 4. What I'd do with another week

- **Real-time-ish stats on the dashboard.** The cheap version is polling — a
  TanStack Query `refetchInterval` of a few seconds. The better version is
  **server-sent events**: the click worker publishes to a Redis pub/sub channel,
  an SSE endpoint subscribes, and the chart updates the moment a click lands. SSE
  over WebSockets because the flow is one-directional (server → browser).
- **Kubernetes hosting with autoscaling.** Today production is a fixed replica
  count on a single EC2 host. A Horizontal Pod Autoscaler would scale the backend
  on CPU / request rate automatically, with managed Postgres and Redis and rolling
  deploys for true zero-downtime.
- The **click rollup table** from section 2, to keep stats fast as data grows.

## 5. Where I used AI — and where I overrode it

AI tooling (Claude Code) was used throughout: scaffolding, boilerplate, test
generation, and the production deployment setup.

The clearest override was structural. The generated `main.go` started as one long,
hard-to-follow wiring blob, and the server package had routing, seeding, and
handlers mixed in the wrong place. I **restructured `main.go` into clear sections**
and **moved the files to where Go's conventions expect them** — routes and seeding
into `internal/`, with handlers, services, and repositories cleanly separated.
Readable wiring and honest package boundaries are the difference between code you
can reason about and code that merely runs.

A sharper, smaller override: the first-draft short-code generator reached for
`math/rand` and `b % 62`. Both are wrong — `math/rand` makes codes guessable, and
modulo over a byte biases the first few letters of the alphabet. I replaced it
with `crypto/rand` and rejection sampling. Easy to miss, and exactly the kind of
thing worth checking rather than trusting.
