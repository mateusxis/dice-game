// Package game holds the room and round use cases plus the round engine that
// drives them.
//
// Layering
// --------
// Use cases here own one business operation each (create a room, join it,
// start a round, place a bet, settle a round, close a room) and talk only to
// the ports. The Engine on top of them owns *time*: it runs a single
// goroutine per room that opens a betting window, waits for the backend clock
// to reach closes_at, settles, and closes the room when the tenth round ends
// or a graceful close is pending.
//
// Money rules enforced across these use cases:
//   - a stake is debited the moment the bet is accepted (ledger type "bet");
//   - winnings are credited only at settlement (ledger type "payout", 1.92x);
//   - every balance mutation runs inside one TxManager transaction that locks
//     the player row with GetForUpdate first;
//   - the client learns its new balance only through balance.updated at round
//     end or room end — never in the bet acknowledgement.
package game

import (
	"context"
	"time"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/game"
)

// Cache and lock lifetimes. PostgreSQL is the source of truth for all of them;
// these TTLs only bound how long a stale Redis entry can survive a crash.
const (
	// roomCacheTTL is how long a cached room summary lives.
	roomCacheTTL = time.Hour
	// sessionTTL bounds the "player is in room X" binding. It is far longer
	// than a full ten-round table so it never expires under a live player, and
	// short enough that a crashed process cannot lock an account out forever.
	sessionTTL = 6 * time.Hour
	// joinLockTTL is the lifetime of the per-room seat-count mutex. Joins take
	// a few milliseconds; this only has to survive a slow database.
	joinLockTTL = 5 * time.Second
	// joinLockAttempts is how many times a join retries the seat mutex before
	// giving up with ErrJoinInProgress.
	joinLockAttempts = 4
	// joinLockBackoff is the pause between seat-mutex attempts.
	joinLockBackoff = 25 * time.Millisecond
)

// Close reasons reported to clients in the room.closed event.
const (
	// ReasonOwnerClosed is an explicit close by the room owner.
	ReasonOwnerClosed = "owner_closed"
	// ReasonMaxRounds is the automatic close after the tenth round settles.
	ReasonMaxRounds = "max_rounds_reached"
	// ReasonShutdown is the abort performed when the server stops with a round
	// still open; open stakes are refunded.
	ReasonShutdown = "server_shutdown"
	// ReasonRecovered is the abort performed at startup for a room a previous
	// process left behind.
	ReasonRecovered = "recovered_after_restart"
	// ReasonSettlementFailed is the abort performed when settlement itself
	// fails (for example the entropy source is unavailable).
	ReasonSettlementFailed = "settlement_failed"
	// BalanceReasonRoundEnd labels the balance push that follows settlement.
	BalanceReasonRoundEnd = "round_end"
	// BalanceReasonRoomEnd labels the balance push that follows a room close.
	BalanceReasonRoomEnd = "room_end"
)

// CreateRoomInput identifies the player opening a table.
type CreateRoomInput struct {
	OwnerID string
}

// CreateRoomOutput carries the freshly created room.
type CreateRoomOutput struct {
	Room ports.RoomSummary
}

// ListOpenRoomsInput pages the open-room listing.
type ListOpenRoomsInput struct {
	Limit  int32
	Offset int32
}

// ListOpenRoomsOutput carries the open rooms and where they came from.
type ListOpenRoomsOutput struct {
	Rooms []ports.RoomSummary
	// Source is "cache" when Redis answered and "database" when PostgreSQL
	// did. It is reported so operators can see the cache working.
	Source string
}

// JoinRoomInput identifies the player and the table they want a seat at.
type JoinRoomInput struct {
	RoomID   string
	PlayerID string
}

// JoinRoomOutput carries the room after the seat was taken.
type JoinRoomOutput struct {
	Room ports.RoomSummary
	// Players is the seat set in join order.
	Players []string
	// AlreadyMember is true when the caller was already seated — a room owner
	// attaching over WebSocket after creating the room over REST, or a client
	// reconnecting. Joining is idempotent, so this is a success, not an error.
	AlreadyMember bool
}

// CloseRoomInput requests a room close.
type CloseRoomInput struct {
	RoomID      string
	RequesterID string
	// Reason is echoed back in the output (and to clients). Empty defaults to
	// ReasonOwnerClosed.
	Reason string
}

// CloseRoomOutput describes the effect of a close request.
type CloseRoomOutput struct {
	RoomID string
	Status game.RoomStatus
	// Closed is true when the room is now terminal; false means the close is
	// pending and takes effect when the live round settles.
	Closed  bool
	Reason  string
	Members []string
	// Balances holds each member's final balance, populated only when the room
	// actually closed, so the caller can push balance.updated.
	Balances map[string]int64
}

// StartRoundInput asks a room to open its next betting window.
type StartRoundInput struct {
	RoomID string
	// RequesterID must be the room owner: only the owner drives the table.
	RequesterID string
}

// StartRoundOutput describes the round that just opened.
type StartRoundOutput struct {
	RoomID      string
	RoundID     string
	RoundNumber int
	OpensAt     time.Time
	ClosesAt    time.Time
	Members     []string
}

// PlaceBetInput is a wager submitted during a betting window.
type PlaceBetInput struct {
	RoomID   string
	PlayerID string
	Choice   game.Choice
	Amount   int64
}

// PlaceBetOutput acknowledges an accepted bet. It deliberately carries no
// balance: the spec requires the bet response to return only the bet, with the
// new balance pushed at round end.
type PlaceBetOutput struct {
	BetID       string
	RoomID      string
	RoundID     string
	RoundNumber int
	Choice      game.Choice
	Amount      int64
}

// SettleRoundInput identifies the room whose live round must be resolved.
type SettleRoundInput struct {
	RoomID string
}

// SettleRoundOutput is the full result of a settled round: what was rolled,
// who won what, and every member's balance afterwards.
type SettleRoundOutput struct {
	RoomID      string
	RoundID     string
	RoundNumber int
	Die1        int
	Die2        int
	Sum         int
	Outcome     game.Outcome
	Results     []game.Result
	Members     []string
	Balances    map[string]int64
	// RoomClosed is true when this settlement also ended the table (tenth
	// round, or a close request that was waiting for the round to finish).
	RoomClosed  bool
	CloseReason string
}

// Winners returns only the winning results, which is what clients are shown.
func (o SettleRoundOutput) Winners() []game.Result {
	winners := make([]game.Result, 0, len(o.Results))
	for _, r := range o.Results {
		if r.Won {
			winners = append(winners, r)
		}
	}
	return winners
}

// RoundState is the read model of a round inside a room snapshot.
type RoundState struct {
	RoundID  string
	Number   int
	Status   game.RoundStatus
	OpensAt  time.Time
	ClosesAt time.Time
	Die1     int
	Die2     int
	Outcome  game.Outcome
}

// RoomState is the snapshot pushed as room.state.
type RoomState struct {
	Room    ports.RoomSummary
	Players []string
	// Round is the most recent round, or nil when none has started.
	Round *RoundState
}

// summaryOf projects a room aggregate onto the cache/listing read model.
func summaryOf(r *game.Room) ports.RoomSummary {
	return ports.RoomSummary{
		ID:           r.ID,
		OwnerID:      r.OwnerID,
		Status:       r.Status,
		CurrentRound: r.CurrentRound,
		MaxRounds:    r.MaxRounds,
		PlayerCount:  r.PlayerCount(),
		MaxPlayers:   r.MaxPlayers,
		CreatedAt:    r.CreatedAt,
	}
}

// memberBalances reads the current balance of every member. It runs inside the
// settlement/close transaction so the balances pushed to clients are exactly
// the ones that were committed.
func memberBalances(ctx context.Context, players ports.PlayerRepository, members []string) (map[string]int64, error) {
	balances := make(map[string]int64, len(members))
	for _, id := range members {
		p, err := players.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		balances[id] = p.Balance
	}
	return balances, nil
}

// attachLiveRound loads a room's most recent round and, when it is still
// taking bets, attaches it to the aggregate. A room with no round at all is
// not an error — it simply has nothing live.
func attachLiveRound(ctx context.Context, rounds ports.RoundRepository, room *game.Room) (*game.Round, error) {
	round, err := rounds.GetCurrent(ctx, room.ID)
	if err != nil {
		if isRoundMissing(err) {
			return nil, nil
		}
		return nil, err
	}
	room.AttachRound(round)
	return round, nil
}
