-- name: BulkInsertClicks :copyfrom
-- Compiles to pgx CopyFrom: one COPY stream instead of N round trips. This is
-- the entire reason the worker accumulates a batch before writing.
INSERT INTO clicks (link_id, clicked_at, referrer, user_agent)
VALUES (@link_id, @clicked_at, @referrer, @user_agent);

-- name: IncrementClickCounts :exec
-- Runs in the same transaction as BulkInsertClicks, so a click row and its
-- counter increment commit or roll back together.
--
-- Static SQL over two parallel arrays rather than a dynamically built VALUES
-- list: one prepared statement, one query plan, no string building.
--
-- Deadlock note: two workers whose batches touch the same links can, in
-- principle, take row locks in opposing orders. The caller sorts link_ids so the
-- common plan (nested loop over v) acquires them in a consistent order, but
-- Postgres does not *guarantee* update order from a subquery, so this reduces
-- the window rather than closing it. If a deadlock does occur, Postgres aborts
-- one transaction, the worker logs it and increments clicks_lost. Losing one
-- batch of analytics to a rare deadlock is an acceptable trade for not holding a
-- global lock on the redirect path.
UPDATE links l
SET click_count = l.click_count + v.n
FROM (
    SELECT unnest(@link_ids::uuid[]) AS id,
           unnest(@counts::bigint[])  AS n
) AS v
WHERE l.id = v.id;

-- name: GetClicksPerDay :many
-- Zero-filled buckets. Without generate_series a day with no clicks is simply
-- absent from the result, and the chart draws a line straight across the gap —
-- which reads as "traffic held steady" when the truth is "nobody clicked".
--
-- The window is passed as an explicit half-open timestamptz range
-- [range_start, range_end) rather than derived from the date bounds in SQL. That
-- keeps the predicate sargable, so Postgres range-scans clicks_link_time_idx
-- instead of evaluating a date expression for every row in the table.
SELECT
    d.day::date                   AS day,
    COALESCE(c.clicks, 0)::bigint AS clicks
FROM generate_series(@from_day::date, @to_day::date, interval '1 day') AS d (day)
LEFT JOIN (
    SELECT date_trunc('day', clicked_at AT TIME ZONE 'UTC')::date AS day,
           count(*) AS clicks
    FROM clicks
    WHERE link_id = @link_id
      AND clicked_at >= @range_start::timestamptz
      AND clicked_at <  @range_end::timestamptz
    GROUP BY 1
) AS c ON c.day = d.day::date
ORDER BY d.day;

-- name: CountClicksForLink :one
-- Used by the integration tests to assert the denormalized links.click_count
-- has not drifted from the raw event table.
SELECT count(*) FROM clicks WHERE link_id = @link_id;
