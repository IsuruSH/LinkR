-- +goose Up

-- citext gives us case-insensitive email uniqueness without a functional index
-- and without every query remembering to lower(). pgcrypto is not needed:
-- gen_random_uuid() is built in as of Postgres 13.
CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         citext NOT NULL UNIQUE,
    password_hash text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE links (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    short_code  text NOT NULL,
    long_url    text NOT NULL,
    -- Denormalized running total. The dashboard lists N links and needs a total
    -- for each; COUNT(*) per row is N index scans per page load, growing without
    -- bound. The click worker increments this in the same transaction as the
    -- click insert, so the counter is exactly as durable as the rows it counts.
    click_count bigint NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT links_short_code_format CHECK (short_code ~ '^[A-Za-z0-9_-]{3,32}$'),
    CONSTRAINT links_click_count_non_negative CHECK (click_count >= 0)
);

-- Uniqueness IS the collision check for generated codes: we insert and let the
-- index reject a duplicate, rather than SELECT-then-INSERT, which races.
CREATE UNIQUE INDEX links_short_code_key ON links (short_code);

-- Serves the keyset pagination in GET /api/links. Column order matches
-- `ORDER BY created_at DESC, id DESC` exactly, so Postgres walks the index
-- rather than sorting.
CREATE INDEX links_user_created_idx ON links (user_id, created_at DESC, id DESC);

CREATE TABLE clicks (
    id         bigserial PRIMARY KEY,
    link_id    uuid NOT NULL REFERENCES links (id) ON DELETE CASCADE,
    clicked_at timestamptz NOT NULL DEFAULT now(),
    -- No IP column on purpose: it is PII, it carries a retention obligation, and
    -- nothing in the product needs it. Referrer and user agent are enough to make
    -- a click row worth storing.
    referrer   text,
    user_agent text
);

-- Serves GET /api/links/{code}/stats: range-scan one link's clicks in a window.
CREATE INDEX clicks_link_time_idx ON clicks (link_id, clicked_at DESC);

-- +goose Down
DROP TABLE IF EXISTS clicks;
DROP TABLE IF EXISTS links;
DROP TABLE IF EXISTS users;
DROP EXTENSION IF EXISTS citext;
