-- +goose Up

-- Optional expiry. NULL means "never expires", which is the vast majority of
-- links, so the column stays sparse.
ALTER TABLE links ADD COLUMN expires_at timestamptz;

-- Partial index: only links that actually expire are indexed. It stays small
-- even though most rows are NULL, and it is what a future "sweep expired links"
-- job would range-scan. The redirect path does not use it — expiry is checked on
-- the already-resolved row, not via a query.
CREATE INDEX links_expires_at_idx ON links (expires_at) WHERE expires_at IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS links_expires_at_idx;
ALTER TABLE links DROP COLUMN expires_at;
