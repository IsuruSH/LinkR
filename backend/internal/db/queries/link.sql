-- name: CreateLink :one
-- No SELECT-then-INSERT: we insert and let links_short_code_key reject a
-- collision. The repository turns 23505 into ErrAliasTaken, and the service
-- retries with a fresh code when the alias was generated rather than chosen.
INSERT INTO links (user_id, short_code, long_url)
VALUES (@user_id, @short_code, @long_url)
RETURNING *;

-- name: GetLinkByShortCode :one
-- The redirect hot path, on cache miss only.
SELECT * FROM links WHERE short_code = @short_code;

-- name: GetLinkByShortCodeForUser :one
-- Ownership is part of the predicate, not a check after the fact: a link that
-- belongs to someone else is indistinguishable from one that does not exist,
-- so stats cannot be used to probe for other users' codes.
SELECT * FROM links WHERE short_code = @short_code AND user_id = @user_id;

-- name: DeleteLinkByShortCode :one
-- RETURNING lets the caller tell "deleted" from "was never yours", without a
-- prior SELECT. Clicks cascade.
DELETE FROM links
WHERE short_code = @short_code AND user_id = @user_id
RETURNING short_code;

-- name: ListLinksFirstPage :many
-- Keyset pagination, page one. Separate from ListLinksAfter because sqlc emits
-- static SQL: a `WHERE ... OR @cursor IS NULL` predicate would collapse into a
-- filter that cannot use links_user_created_idx. Two honest queries beat one
-- query that quietly stops using its index.
-- Callers pass page_size = limit + 1 and use the extra row to detect "has more".
SELECT * FROM links
WHERE user_id = @user_id
ORDER BY created_at DESC, id DESC
LIMIT @page_size;

-- name: ListLinksAfter :many
-- Row-value comparison, not `created_at < x OR (created_at = x AND id < y)`.
-- Postgres can drive links_user_created_idx directly from a row constructor,
-- and it is correct when two links share a created_at to the microsecond.
SELECT * FROM links
WHERE user_id = @user_id
  AND (created_at, id) < (@after_created_at::timestamptz, @after_id::uuid)
ORDER BY created_at DESC, id DESC
LIMIT @page_size;
