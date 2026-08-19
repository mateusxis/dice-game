-- name: CreatePlayer :one
INSERT INTO players (id, email, password_hash, balance, created_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, email, password_hash, balance, created_at;

-- name: GetPlayerByID :one
SELECT id, email, password_hash, balance, created_at
FROM players
WHERE id = $1;

-- name: GetPlayerByEmail :one
SELECT id, email, password_hash, balance, created_at
FROM players
WHERE lower(email) = lower(sqlc.arg(email)::text);

-- name: GetPlayerForUpdate :one
-- Row-level lock taken before every balance mutation. Must run inside a
-- transaction; serializes concurrent deposits/withdrawals/bets per player.
SELECT id, email, password_hash, balance, created_at
FROM players
WHERE id = $1
FOR UPDATE;

-- name: UpdatePlayerBalance :one
UPDATE players
SET balance = $2
WHERE id = $1
RETURNING id, email, password_hash, balance, created_at;

-- name: AddPlayerBalance :one
-- Atomic relative adjustment; the CHECK constraint rejects a negative result.
UPDATE players
SET balance = balance + sqlc.arg(delta)::bigint
WHERE id = sqlc.arg(id)
RETURNING id, email, password_hash, balance, created_at;
