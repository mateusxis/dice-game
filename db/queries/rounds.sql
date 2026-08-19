-- name: CreateRound :one
INSERT INTO rounds (id, room_id, number, betting_opens_at, betting_closes_at, status)
VALUES ($1, $2, $3, $4, $5, 'betting')
RETURNING id, room_id, number, betting_opens_at, betting_closes_at, die1, die2, outcome, status, created_at, settled_at;

-- name: GetRoundByID :one
SELECT id, room_id, number, betting_opens_at, betting_closes_at, die1, die2, outcome, status, created_at, settled_at
FROM rounds
WHERE id = $1;

-- name: GetRoundByRoomAndNumber :one
SELECT id, room_id, number, betting_opens_at, betting_closes_at, die1, die2, outcome, status, created_at, settled_at
FROM rounds
WHERE room_id = $1 AND number = $2;

-- name: GetCurrentRound :one
SELECT id, room_id, number, betting_opens_at, betting_closes_at, die1, die2, outcome, status, created_at, settled_at
FROM rounds
WHERE room_id = $1
ORDER BY number DESC
LIMIT 1;

-- name: ListRoundsByRoom :many
SELECT id, room_id, number, betting_opens_at, betting_closes_at, die1, die2, outcome, status, created_at, settled_at
FROM rounds
WHERE room_id = $1
ORDER BY number ASC;

-- name: SettleRound :one
UPDATE rounds
SET die1 = sqlc.arg(die1),
    die2 = sqlc.arg(die2),
    outcome = sqlc.arg(outcome),
    status = 'settled',
    settled_at = sqlc.arg(settled_at)
WHERE id = sqlc.arg(id) AND status = 'betting'
RETURNING id, room_id, number, betting_opens_at, betting_closes_at, die1, die2, outcome, status, created_at, settled_at;
