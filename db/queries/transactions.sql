-- name: InsertTransaction :one
INSERT INTO transactions (id, player_id, type, amount, round_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, player_id, type, amount, round_id, created_at;

-- name: ListTransactionsByPlayer :many
SELECT id, player_id, type, amount, round_id, created_at
FROM transactions
WHERE player_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: SumTransactionsByPlayer :one
-- Ledger reconciliation helper: the signed sum must equal players.balance.
SELECT COALESCE(SUM(
    CASE
        WHEN type IN ('deposit', 'payout') THEN amount
        ELSE -amount
    END
), 0)::bigint AS net
FROM transactions
WHERE player_id = $1;
