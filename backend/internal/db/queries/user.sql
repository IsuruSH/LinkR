-- name: CreateUser :one
-- The UNIQUE index on email is the duplicate check. Racing registrations both
-- reach here; one gets 23505, which the repository maps to ErrEmailTaken.
INSERT INTO users (email, password_hash)
VALUES (@email, @password_hash)
RETURNING *;

-- name: GetUserByEmail :one
-- email is citext, so this is case-insensitive without lower() at the call site.
SELECT * FROM users WHERE email = @email;
