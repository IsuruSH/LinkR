# Decisions

What I chose, what I gave up, and where it breaks.

---

## 1. The four decisions that mattered

### Redis on the hot path, with Postgres as the fallback — not an in-memory cache

The brief offers "an in-memory cache for the redirect lookup, with correct invalidation" as a stretch goal. I read those last three words as ruling out the in-memory version.

The moment the backend is stateless enough to run `--scale backend=3`, a per-process map has no invalidation story. Replica A deletes a link and clears its own map; replicas B and C keep serving the dead link until their TTL expires. You cannot fix that without inventing a cache-invalidation bus, at which point you have built a worse Redis.

So: `code → {id, url}` in Redis, 24h TTL, filled on miss, `DEL`'d on delete.

Three details that are easy to get wrong:

- **The cached value carries the link ID, not just the URL.** The redirect needs `link_id` to attribute the click. Caching only the URL means every *cache hit* still takes a Postgres read to resolve the ID — which defeats the cache entirely, while looking like it works.
- **Misses are negative-cached** for 60s under an empty sentinel. Without this, scanning `/aaaaaaa`, `/aaaaaab`, … misses Redis every time and lands on Postgres. Cheap DoS, or just an impatient crawler.
- **Redis being down is not fatal.** `Resolve` logs a warning and reads Postgres. `/readyz` reports `degraded` and the replica stays in rotation. A slow redirect beats a broken one. Losing the cache should cost latency, not availability.

**Traded away:** an extra network hop on every miss, a second thing to operate, and a genuine correctness gap — a `Resolve` that read the row just before a concurrent `Delete` can write its cache entry just after the `DEL`, resurrecting a deleted link for one TTL. Closing that needs a version stamp or a delete marker. The exposure is one in-flight request; I documented it rather than papering over it.

### Async click recording: drop, don't block

Covered in full in §3. The short version: the handler writes the 302 and *then* offers the event to a bounded channel with `select`/`default`. It never blocks, and a full buffer drops the event and increments a counter.

### Keyset pagination, which forced two queries instead of one

Cursor is `base64(created_at | id)`; the predicate is the row constructor `(created_at, id) < ($2, $3)`, matching `links_user_created_idx (user_id, created_at DESC, id DESC)`.

Offset pagination degrades linearly with depth, and — more damning here — it *skips rows*. The dashboard creates links while you are paging through them; with `OFFSET 5`, a link created between page 1 and page 2 pushes a row down onto both. There is an integration test for exactly that.

`created_at` alone is not a valid cursor: two links created in the same microsecond would make the page boundary ambiguous. The `id` breaks the tie, and the cursor encodes the timestamp as RFC3339**Nano** so Postgres's microsecond precision survives the round trip.

The cost is real and worth naming: **sqlc emits static SQL, so "first page" and "page after cursor" are two separate queries.** A single query with `WHERE ... OR $2 IS NULL` would collapse into a filter that cannot use the index. I verified the plan rather than assuming it — over 20k rows, the row constructor lands in `Index Cond`, not a post-scan `Filter`, with no sort node:

```
Limit
  ->  Index Scan using links_user_created_idx on links
        Index Cond: ((user_id = '…') AND (ROW(created_at, id) < ROW('…', '…')))
```

**Traded away:** no random page access, and no total count without a second query. The dashboard displays neither.

### JWT in an httpOnly cookie, via a BFF proxy

`localStorage` is readable by any XSS. One compromised dependency exfiltrates every user's session. So the token goes in an `httpOnly` + `SameSite=Lax` cookie, set by a Next route handler; the login response body carries only `{user}`.

Most write-ups stop there and skip the consequence: **client JavaScript then cannot read the token to set an `Authorization` header.** You either weaken the cookie so JS can read it — undoing the whole point — or you put a server between the browser and the API. So the browser calls `/api/bff/*`, a same-origin Next route handler that reads the cookie server-side and forwards a Bearer token to Go.

`proxy.ts` (Next 16's successor to `middleware.ts`) checks only that the cookie *exists*. It does not verify the signature, because that would mean shipping the signing secret to the edge runtime. It is a UX affordance to avoid rendering a dashboard that is about to 401 — **Go is the security boundary**, and a forged cookie earns a 401 there.

**Traded away:** one extra hop inside the cluster, and the API is now slightly coupled to having a trusted server-side caller. A native mobile client would use the bearer token directly, which the API already supports — that is why `/api/auth/login` returns the token rather than setting the cookie itself.

---

## 2. Designing for heavy traffic and concurrent load

**What the load actually is.** The redirect is ~99% of requests and is read-mostly. Everything else is a dashboard endpoint with a human behind it. So the design optimizes exactly one path and lets the rest be ordinary.

**What holds up:**

| | |
|---|---|
| **Stateless backend** | No session affinity, no in-process cache to keep coherent. `--scale backend=3` behind any L7 LB works today. Migrations and the seed take a Postgres advisory lock so N replicas booting at once means one applies and the rest no-op. |
| **The redirect is one Redis GET** | On a hit it never touches Postgres. |
| **Writes are batched and off the request path** | `CopyFrom` streams a batch of clicks; one `UPDATE … FROM unnest($1::uuid[], $2::bigint[])` fixes up the counters. One transaction, one round trip each. |
| **`links.click_count` is denormalized** | The dashboard needs a total per row. `COUNT(*)` per link is N index scans per page load, growing forever. The worker increments the counter in the *same transaction* as the click insert, so it cannot drift from the rows it counts — asserted directly in the integration tests, including under four workers writing overlapping batches. |
| **Pool sized on purpose** | `MaxConns=20`. Postgres defaults to `max_connections=100`, so four replicas fit with room for migrations and a `psql`. Work is short and I/O-bound; past ~2–3× the database's core count, more connections queue *inside* Postgres rather than adding throughput. `MaxConnLifetime=30m` lets connections migrate to a new primary after failover instead of pinning to a dead one. |

**What breaks first, in order:**

1. **The `clicks` table.** It is an append-only event log with no retention. At sustained high traffic it becomes the largest object in the database, and the stats query's `GROUP BY day` scans every row in the window. **Fix:** a `click_daily_rollup (link_id, day, count)` table maintained by the same worker in the same transaction, plus monthly partitioning on `clicks` with old partitions dropped. The stats query then reads the rollup and only touches raw events for today. This is the first thing I would build.
2. **Postgres write throughput.** Every click is eventually a row. Batching moved the ceiling but did not remove it. **Fix:** the rollup above turns N inserts into one upsert per link per day. After that, a queue in front of the workers.
3. **The single Postgres primary.** **Fix:** read replicas. The insertion point already exists — sqlc generates `db.New(DBTX)`, and `DBTX` is satisfied by `*pgxpool.Pool` and `pgx.Tx` alike. A read/write split is `db.New(replicaPool)` in `main.go`, a constructor change, not a rewrite. There is no `replica/` package today because there is no read replica today.
4. **Redis as a single point of latency.** Already degrades to Postgres rather than failing. Beyond that: Redis Cluster, or a small per-process LRU in *front* of Redis for the hottest few hundred codes, accepting a few seconds of staleness on delete.

**Graceful degradation, concretely:** Redis down → redirects get slower, nothing breaks. Click buffer full → analytics get lossy, redirects stay fast. Postgres down → `/readyz` fails and the replica leaves the load balancer; cached redirects keep working until their TTL expires.

---

## 3. How async click recording works, and what happens under load or on crash

```
Handler:  resolve code (Redis → Postgres → cache-fill)
          write the 302                              ← the browser is now gone
          select { case ch <- ClickEvent: default: dropped++ ; warn }

Worker:   4 goroutines drain a bounded channel (10 000)
          flush at 100 events OR 500ms, whichever comes first
          flush = ONE tx: CopyFrom(clicks) + UPDATE links.click_count
```

**The redirect never waits on the write.** Not "usually doesn't" — there is a test that would fail if it did. With the batch size and flush interval set so nothing *can* flush, 20 redirects serve in **6.03 ms with zero rows persisted**; after the drain, all 20 are there. And driving the real stack: 200 parallel redirects, of which **187 clicks were durable the instant the last 302 returned** — the other 13 were still in the buffer.

**Under load: we drop, and we count.** A full buffer means the database is already behind. Blocking the redirect would queue HTTP handlers behind a write-path backlog, converting a degraded analytics pipeline into a user-facing outage — and a *longer* one, since the queue keeps growing. `clicks_dropped_total` makes the loss visible rather than silent, and the warning log is rate-limited to once a second so an overload does not become a disk-I/O problem too.

The alternative — spilling to a Redis list as a durable buffer — trades an in-process drop for a network write on the hot path and a second thing that can be full. It is the right call when clicks are billable. Here they are analytics.

**On crash: yes, we lose clicks.** Up to 10 000 buffered events, or ≤500 ms of accumulation, whichever is smaller. Plus anything in flight between `http.Redirect` returning and the channel send.

**Is that acceptable?** For click analytics on a URL shortener, yes, and I would defend that in a design review. A dashboard that says 41,203 instead of 41,207 tells the user exactly the same thing. The failure mode is bounded, observable, and proportional to the outage. If clicks were billable — an ad network, say — this would be indefensible, and the answer is Redis Streams: `XADD` on the hot path (one network write, still no Postgres), a consumer group in the worker, `XACK` after the transaction commits. That buys at-least-once delivery and a buffer that survives a process restart.

**The swap is a one-package change**, by construction. Handlers only ever see `worker.Recorder` — a single-method interface. The channel, the pool, and the batching all live behind it. Nothing in `handler/` or `service/` knows a channel exists.

**Shutdown ordering is the whole trick:**

```
SIGTERM
  → srv.Shutdown(ctx)      stop accepting; in-flight handlers finish
  → close(clickCh)         safe ONLY because ↑ guarantees nobody is still sending
  → workers drain          bounded by CLICK_DRAIN_TIMEOUT (5s)
  → redis.Close(), pool.Close()
```

Reversing the first two panics on send-to-closed-channel. It is the classic version of this bug, it only fires under load, and it looks like a random crash on deploy. `srv.Shutdown` returning is the *only* reason `close()` is safe — that is why the ordering is commented in `main.go` rather than left to be rediscovered.

A drain that exceeds its deadline logs the loss and exits anyway. A shutdown that waits forever on a sick database never completes, and the orchestrator `SIGKILL`s you regardless — better to lose the events knowingly.

---

## 4. With another week

In the order I would actually do them:

1. **`click_daily_rollup` + monthly partitions on `clicks`.** The first real scaling limit, and the cheapest to remove. The worker already holds the transaction where the rollup upsert belongs.
2. **Prometheus `/metrics`.** `clicks_dropped_total` and `clicks_lost_total` are private atomics behind `worker.Stats()` today. They are the two numbers that tell you the pipeline is unhealthy, and nobody can see them. Redirect latency histogram and cache hit ratio next. The middleware chain has the seam.
3. **Redis Streams behind `worker.Recorder`.** As above. It also decouples the worker from the API process entirely, so clicks keep buffering during a backend deploy.
4. **Per-user rate limiting on `POST /api/links`.** Redis `INCR` + `EXPIRE`, fixed window, `429` + `Retry-After`. Fixed window permits a 2× burst across the boundary; a Lua token bucket fixes that if it matters. It slots into the middleware chain and needs no schema change.
5. **Link expiration.** `expires_at`, `410 Gone`, and — the interesting part — the cache TTL becomes `min(CACHE_TTL, time until expiry)`, so Redis cannot serve a link past its own death.
6. **Close the delete/cache race** with a version stamp on the cache key.
7. **OpenTelemetry.** The request ID already threads through every log line; spans are the natural next step.
8. **CI** (`go vet`, `golangci-lint`, `go test -race`, `docker build`). Deliberately skipped here, but it is what turns "the tests pass on my machine" into a claim a reviewer can check.

And one thing I would *undo*: the frontend hand-writes its wire types in `types/api.ts` to mirror the Go DTOs. That is fine for six endpoints and a lie waiting to happen at twenty. I would emit an OpenAPI spec from the handlers and generate the client.

---

## 5. Where I used AI, and where I overrode it

I used Claude throughout — scaffolding, boilerplate, first drafts of tests, and as a rubber duck for the shutdown ordering. It is very good at producing code that looks right.

**The override worth describing is the short-code generator.** The first draft was the version everyone writes:

```go
const charset = "ABC…xyz0123456789"
b := make([]byte, 7)
for i := range b {
    b[i] = charset[rand.Intn(len(charset))]   // math/rand
}
```

Two bugs, and they are different bugs.

**`math/rand` is predictable.** Seeded from the clock, it is not a secret. Anyone who watches a few codes get issued can enumerate the ones issued after them. Short codes are capability URLs — the code *is* the authorization — so this is a real vulnerability, not a purity argument. Fixed with `crypto/rand`.

**And `b % 62` is biased**, which is subtler. A byte holds 0–255. 256 is not a multiple of 62, so folding it maps 0–193 onto four source values each and 194–255 onto three: the first eight letters of the alphabet come up ~25% more often than the rest. It shrinks the effective keyspace and concentrates collisions.

The fix is rejection sampling: discard any byte ≥ 248 (the largest multiple of 62 that fits in a byte) and fold the rest. About 3% of bytes are thrown away.

I did not want to take my own word for it, so `TestGenerateShortCode_DistributionIsUnbiased` runs a chi-square goodness-of-fit test over 420,000 sampled characters. Against a threshold of 112 (df=61, p=0.001), the modulo generator scores **3085.7** and the rejection sampler scores **59.7**. If someone later "simplifies" the loop back to `% 62`, that test fails loudly and explains why in its failure message.

**Three smaller corrections**, all found by actually running things rather than reading them:

- I wrote a comment claiming `ORDER BY 1` inside a subquery gave the counter `UPDATE` a deterministic row-lock order. It does not — Postgres makes no such guarantee about update order from a subquery. I removed the `ORDER BY`, sorted the IDs in Go instead (which narrows the window without closing it), and rewrote the comment to say so honestly. A comment that overstates a guarantee is worse than no comment.
- shadcn's `Dialog` traps focus correctly but emits no `aria-modal`, and nothing behind the overlay gets `aria-hidden` — so a screen-reader user could browse the dashboard underneath an open modal. Found by asserting it in Playwright, not by reading the source.
- The chart's dark-mode blue: the obvious move is to lighten the light-mode color. `#60a5fa` fails the dark lightness band. The dark step has to be *chosen* against the dark surface, not derived from the light one. I ran the palette through a validator instead of trusting my eyes.

The pattern in all four: AI wrote plausible code, and the errors were in the places where plausible and correct diverge — statistical bias, a guarantee the database does not make, an ARIA attribute nobody sees, a color that looks fine on the monitor I happened to be using. That is the part of the job that does not go away.
