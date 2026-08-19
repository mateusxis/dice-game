package game

import (
	"strings"
	"time"
)

// Table limits fixed by the product spec.
const (
	// MaxPlayers is the seat cap for a single room.
	MaxPlayers = 6
	// MaxRounds is the number of rounds a room plays before auto-closing.
	MaxRounds = 10
	// DefaultBettingWindow is the length of each round's betting period.
	DefaultBettingWindow = 15 * time.Second
)

// RoomStatus is the lifecycle state of a room.
//
// State machine:
//
//	open ──Close()──▶ closing ──(current round settles)──▶ closed
//	  │                                                      ▲
//	  └──────────Close() with no live round──────────────────┘
//
// A room also transitions open → closed automatically once round MaxRounds
// has been settled. "closing" exists so an owner's close request never cuts a
// live round short: bets already placed are always resolved.
type RoomStatus string

const (
	// RoomOpen accepts joins, round starts and bets.
	RoomOpen RoomStatus = "open"
	// RoomClosing has a graceful close pending: the current round finishes,
	// then the room closes. No new joins or rounds.
	RoomClosing RoomStatus = "closing"
	// RoomClosed is terminal.
	RoomClosed RoomStatus = "closed"
)

// Valid reports whether s is a known room status.
func (s RoomStatus) Valid() bool {
	return s == RoomOpen || s == RoomClosing || s == RoomClosed
}

// Room is the aggregate root for a table. It owns the seat set, the round
// counter and the lifecycle state machine. It holds at most one live round at
// a time; the application layer drives it from a single per-room goroutine, so
// the aggregate itself is not safe for concurrent use by design.
type Room struct {
	ID      string
	OwnerID string
	Status  RoomStatus

	MaxRounds  int
	MaxPlayers int

	// CurrentRound is the number of the most recently started round (0 before
	// the first one starts).
	CurrentRound int

	CreatedAt time.Time
	ClosedAt  *time.Time

	players map[string]time.Time // playerID -> joinedAt
	round   *Round               // the live round, nil when none is in progress
}

// NewRoom creates an open room owned by ownerID, who is seated immediately as
// its first member.
func NewRoom(id, ownerID string, createdAt time.Time) (*Room, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(ownerID) == "" {
		return nil, ErrInvalidID
	}
	r := &Room{
		ID:           id,
		OwnerID:      ownerID,
		Status:       RoomOpen,
		MaxRounds:    MaxRounds,
		MaxPlayers:   MaxPlayers,
		CurrentRound: 0,
		CreatedAt:    createdAt.UTC(),
		players:      map[string]time.Time{ownerID: createdAt.UTC()},
	}
	return r, nil
}

// RehydrateRoom rebuilds a room from persisted state.
func RehydrateRoom(id, ownerID string, status RoomStatus, currentRound int, createdAt time.Time, closedAt *time.Time, players map[string]time.Time, liveRound *Round) (*Room, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(ownerID) == "" {
		return nil, ErrInvalidID
	}
	if !status.Valid() {
		return nil, ErrRoomNotOpen
	}
	if players == nil {
		players = map[string]time.Time{}
	}
	seats := make(map[string]time.Time, len(players))
	for id, at := range players {
		seats[id] = at.UTC()
	}
	return &Room{
		ID:           id,
		OwnerID:      ownerID,
		Status:       status,
		MaxRounds:    MaxRounds,
		MaxPlayers:   MaxPlayers,
		CurrentRound: currentRound,
		CreatedAt:    createdAt.UTC(),
		ClosedAt:     closedAt,
		players:      seats,
		round:        liveRound,
	}, nil
}

// PlayerCount returns the number of seated players.
func (r *Room) PlayerCount() int { return len(r.players) }

// HasPlayer reports membership.
func (r *Room) HasPlayer(playerID string) bool {
	_, ok := r.players[playerID]
	return ok
}

// Players returns the seated player ids in join order.
func (r *Room) Players() []string {
	out := make([]string, 0, len(r.players))
	for id := range r.players {
		out = append(out, id)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && r.players[out[j]].Before(r.players[out[j-1]]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// IsOwner reports whether playerID owns the room.
func (r *Room) IsOwner(playerID string) bool { return r.OwnerID == playerID }

// Full reports whether every seat is taken.
func (r *Room) Full() bool { return len(r.players) >= r.MaxPlayers }

// Active reports whether the room still accepts play (open or gracefully
// closing with a live round).
func (r *Room) Active() bool { return r.Status != RoomClosed }

// CurrentRoundRef returns the live round, or nil when none is in progress.
func (r *Room) CurrentRoundRef() *Round { return r.round }

// AttachRound re-attaches a round loaded from storage to a room loaded from
// storage. Repositories rehydrate the two aggregates separately (a room row
// plus its seats, a round row plus its bets), so the application layer uses
// this to reassemble them before driving the lifecycle — Join/StartRound/
// PlaceBet/SettleRound all reason about r.round. Passing a settled round, or
// nil, clears the live round.
func (r *Room) AttachRound(round *Round) {
	if round == nil || round.Status == RoundSettled {
		r.round = nil
		return
	}
	r.round = round
}

// Join seats a player. It rejects a closed or closing room, a full room and a
// duplicate join. The 6-seat cap is also guarded outside the aggregate by a
// Redis lock plus the room_players primary key, since concurrent joins from
// different connections never share this in-memory instance.
func (r *Room) Join(playerID string, now time.Time) error {
	if strings.TrimSpace(playerID) == "" {
		return ErrInvalidID
	}
	switch r.Status {
	case RoomClosed:
		return ErrRoomClosed
	case RoomClosing:
		return ErrRoomNotOpen
	}
	if _, exists := r.players[playerID]; exists {
		return ErrAlreadyJoined
	}
	if r.Full() {
		return ErrRoomFull
	}
	r.players[playerID] = now.UTC()
	return nil
}

// Leave removes a player from the room. The owner leaving does not close the
// room; closing is an explicit owner action.
func (r *Room) Leave(playerID string) error {
	if _, exists := r.players[playerID]; !exists {
		return ErrNotAMember
	}
	delete(r.players, playerID)
	return nil
}

// StartRound opens the next round's betting window. It refuses when the room
// is not open, when a round is already live, when the round cap is reached or
// when the room is empty.
func (r *Room) StartRound(roundID string, now time.Time, window time.Duration) (*Round, error) {
	if r.Status != RoomOpen {
		if r.Status == RoomClosed {
			return nil, ErrRoomClosed
		}
		return nil, ErrRoomNotOpen
	}
	if r.round != nil && r.round.Status != RoundSettled {
		return nil, ErrRoundInProgress
	}
	if len(r.players) == 0 {
		return nil, ErrNoPlayers
	}
	if r.CurrentRound >= r.MaxRounds {
		return nil, ErrMaxRoundsReached
	}
	round, err := NewRound(roundID, r.ID, r.CurrentRound+1, now, window)
	if err != nil {
		return nil, err
	}
	r.CurrentRound = round.Number
	r.round = round
	return round, nil
}

// PlaceBet validates membership, room state and the betting window, then
// records the bet on the live round.
func (r *Room) PlaceBet(bet *Bet, now time.Time) error {
	if bet == nil {
		return ErrInvalidID
	}
	if !r.Active() {
		return ErrRoomClosed
	}
	if !r.HasPlayer(bet.PlayerID) {
		return ErrNotAMember
	}
	if r.round == nil {
		return ErrNoActiveRound
	}
	return r.round.PlaceBet(bet, now)
}

// SettleRound rolls the dice for the live round and resolves its bets. When
// the room is gracefully closing, or when the round cap has just been
// reached, the room transitions to closed as part of the same call — this is
// the "a room closes only when the current round ends" rule.
func (r *Room) SettleRound(roller DiceRoller, now time.Time) ([]Result, error) {
	if r.round == nil {
		return nil, ErrNoActiveRound
	}
	if r.Status == RoomClosed {
		return nil, ErrRoomClosed
	}
	results, err := r.round.Settle(roller, now)
	if err != nil {
		return nil, err
	}
	r.finishRound(now)
	return results, nil
}

// SettleRoundWithDice is SettleRound with pre-determined dice (tests/replay).
func (r *Room) SettleRoundWithDice(die1, die2 int, now time.Time) ([]Result, error) {
	if r.round == nil {
		return nil, ErrNoActiveRound
	}
	if r.Status == RoomClosed {
		return nil, ErrRoomClosed
	}
	results, err := r.round.SettleWithDice(die1, die2, now)
	if err != nil {
		return nil, err
	}
	r.finishRound(now)
	return results, nil
}

func (r *Room) finishRound(now time.Time) {
	if r.Status == RoomClosing || r.CurrentRound >= r.MaxRounds {
		r.markClosed(now)
	}
}

// Close requests a graceful shutdown of the room. Only the owner may do it.
// With a live round it moves the room to "closing" and the room actually
// closes when that round settles; with no live round it closes immediately.
// Closing an already-closed room is an error; re-requesting a close on a
// closing room is a no-op.
func (r *Room) Close(requesterID string, now time.Time) error {
	if !r.IsOwner(requesterID) {
		return ErrNotOwner
	}
	switch r.Status {
	case RoomClosed:
		return ErrRoomClosed
	case RoomClosing:
		return nil
	}
	if r.round != nil && r.round.Status != RoundSettled {
		r.Status = RoomClosing
		return nil
	}
	r.markClosed(now)
	return nil
}

// ForceClose closes the room without owner or round checks. It exists for
// administrative/error paths (e.g. an entropy failure aborts the table).
func (r *Room) ForceClose(now time.Time) {
	if r.Status != RoomClosed {
		r.markClosed(now)
	}
}

func (r *Room) markClosed(now time.Time) {
	closedAt := now.UTC()
	r.Status = RoomClosed
	r.ClosedAt = &closedAt
}

// RoundsRemaining returns how many rounds the room may still start.
func (r *Room) RoundsRemaining() int {
	remaining := r.MaxRounds - r.CurrentRound
	if remaining < 0 {
		return 0
	}
	return remaining
}
