-- name: CreateRoom :one
INSERT INTO rooms (id, owner_id, status, max_rounds, max_players, current_round, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, owner_id, status, max_rounds, max_players, current_round, created_at, closed_at;

-- name: GetRoomByID :one
SELECT id, owner_id, status, max_rounds, max_players, current_round, created_at, closed_at
FROM rooms
WHERE id = $1;

-- name: GetRoomForUpdate :one
SELECT id, owner_id, status, max_rounds, max_players, current_round, created_at, closed_at
FROM rooms
WHERE id = $1
FOR UPDATE;

-- name: ListOpenRooms :many
SELECT r.id,
       r.owner_id,
       r.status,
       r.max_rounds,
       r.max_players,
       r.current_round,
       r.created_at,
       (SELECT count(*) FROM room_players rp WHERE rp.room_id = r.id)::int AS player_count
FROM rooms r
WHERE r.status = 'open'
ORDER BY r.created_at DESC
LIMIT $1 OFFSET $2;

-- name: UpdateRoomStatus :one
UPDATE rooms
SET status = sqlc.arg(status),
    closed_at = sqlc.narg(closed_at)
WHERE id = sqlc.arg(id)
RETURNING id, owner_id, status, max_rounds, max_players, current_round, created_at, closed_at;

-- name: UpdateRoomCurrentRound :one
UPDATE rooms
SET current_round = $2
WHERE id = $1
RETURNING id, owner_id, status, max_rounds, max_players, current_round, created_at, closed_at;

-- name: AddRoomPlayer :one
INSERT INTO room_players (room_id, player_id, joined_at)
VALUES ($1, $2, $3)
RETURNING room_id, player_id, joined_at;

-- name: RemoveRoomPlayer :execrows
DELETE FROM room_players
WHERE room_id = $1 AND player_id = $2;

-- name: ListRoomPlayers :many
SELECT room_id, player_id, joined_at
FROM room_players
WHERE room_id = $1
ORDER BY joined_at ASC;

-- name: CountRoomPlayers :one
SELECT count(*)::int AS player_count
FROM room_players
WHERE room_id = $1;

-- name: FindActiveRoomForPlayer :one
-- "One room at a time": returns the non-closed room the player currently sits
-- in. Redis holds the fast path; this is the authoritative check.
SELECT r.id
FROM room_players rp
JOIN rooms r ON r.id = rp.room_id
WHERE rp.player_id = $1
  AND r.status <> 'closed'
ORDER BY rp.joined_at DESC
LIMIT 1;

-- name: ListActiveRoomIDs :many
-- Crash recovery: every room a previous process left behind (not closed), so
-- the new process can refund open bets and close them.
SELECT id
FROM rooms
WHERE status <> 'closed'
ORDER BY created_at ASC;
