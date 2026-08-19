-- name: InsertBet :one
-- The (round_id, player_id) unique constraint makes a duplicate submission
-- fail here rather than in application code, which is what actually protects
-- against two concurrent requests from the same player.
INSERT INTO bets (id, round_id, player_id, choice, amount, placed_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, round_id, player_id, choice, amount, won, payout, placed_at, settled_at;

-- name: GetBetByRoundAndPlayer :one
SELECT id, round_id, player_id, choice, amount, won, payout, placed_at, settled_at
FROM bets
WHERE round_id = $1 AND player_id = $2;

-- name: ListBetsByRound :many
SELECT id, round_id, player_id, choice, amount, won, payout, placed_at, settled_at
FROM bets
WHERE round_id = $1
ORDER BY placed_at ASC;

-- name: ListBetsByPlayer :many
SELECT id, round_id, player_id, choice, amount, won, payout, placed_at, settled_at
FROM bets
WHERE player_id = $1
ORDER BY placed_at DESC
LIMIT $2 OFFSET $3;

-- name: SettleBet :one
UPDATE bets
SET won = sqlc.arg(won),
    payout = sqlc.arg(payout),
    settled_at = sqlc.arg(settled_at)
WHERE id = sqlc.arg(id) AND won IS NULL
RETURNING id, round_id, player_id, choice, amount, won, payout, placed_at, settled_at;
