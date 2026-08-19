package ws

import (
	"encoding/json"
	"time"
)

// Event names. Client→server events are commands; server→client events are
// either the direct answer to a command or a push from the round engine.
//
// client → server
//
//	auth          {"token": "<jwt>"}                      (only when the
//	                                                       handshake carried no
//	                                                       token)
//	room.join     {"room_id": "<uuid>"}
//	room.start    {}                                       owner only, alias of
//	                                                       round.start
//	round.start   {}                                       owner only
//	bet.place     {"choice": "even"|"odd", "amount": 1000}
//
// server → client
//
//	room.state       full room snapshot
//	round.started    {room_id, round_id, round_number, opens_at, closes_at}
//	bet.accepted     {bet_id, room_id, round_id, round_number, choice, amount}
//	round.result     {room_id, round_id, round_number, die1, die2, sum,
//	                  outcome, winners[], room_closed}
//	balance.updated  {player_id, room_id, balance, reason}
//	room.closed      {room_id, reason}
//	error            {code, message, event}
const (
	// EventAuth carries a token when the handshake did not.
	EventAuth = "auth"
	// EventRoomJoin seats the caller in a room.
	EventRoomJoin = "room.join"
	// EventRoomStart is the owner's "start the table" command; it starts the
	// next round, so it behaves exactly like EventRoundStart.
	EventRoomStart = "room.start"
	// EventRoundStart opens the next betting window (owner only).
	EventRoundStart = "round.start"
	// EventBetPlace submits a wager for the live round.
	EventBetPlace = "bet.place"

	// EventRoomState is a full room snapshot.
	EventRoomState = "room.state"
	// EventRoundStarted announces a new betting window.
	EventRoundStarted = "round.started"
	// EventBetAccepted acknowledges a bet — deliberately without a balance.
	EventBetAccepted = "bet.accepted"
	// EventRoundResult announces the dice and the winners.
	EventRoundResult = "round.result"
	// EventBalanceUpdated pushes a player's balance at round end or room end.
	EventBalanceUpdated = "balance.updated"
	// EventRoomClosed announces a terminal room.
	EventRoomClosed = "room.closed"
	// EventError reports a rejected command.
	EventError = "error"
)

// inbound is the envelope of a client→server message.
type inbound struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// outbound is the envelope of a server→client message.
type outbound struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
}

// authData carries a bearer token supplied as the first message.
type authData struct {
	Token string `json:"token"`
}

// joinData is the room.join payload.
type joinData struct {
	RoomID string `json:"room_id"`
}

// betData is the bet.place payload. Amount is in cents, like every other
// monetary value in the API.
type betData struct {
	Choice string `json:"choice"`
	Amount int64  `json:"amount"`
}

// RoundPayload describes a round inside a room snapshot.
type RoundPayload struct {
	RoundID  string    `json:"round_id"`
	Number   int       `json:"round_number"`
	Status   string    `json:"status"`
	OpensAt  time.Time `json:"opens_at"`
	ClosesAt time.Time `json:"closes_at"`
	Die1     int       `json:"die1,omitempty"`
	Die2     int       `json:"die2,omitempty"`
	Outcome  string    `json:"outcome,omitempty"`
}

// RoomStatePayload is the room.state event body.
type RoomStatePayload struct {
	RoomID       string        `json:"room_id"`
	OwnerID      string        `json:"owner_id"`
	Status       string        `json:"status"`
	CurrentRound int           `json:"current_round"`
	MaxRounds    int           `json:"max_rounds"`
	PlayerCount  int           `json:"player_count"`
	MaxPlayers   int           `json:"max_players"`
	Players      []string      `json:"players"`
	Round        *RoundPayload `json:"round,omitempty"`
}

// RoundStartedPayload is the round.started event body. closes_at is the
// backend's authoritative deadline; clients must not compute their own.
type RoundStartedPayload struct {
	RoomID   string    `json:"room_id"`
	RoundID  string    `json:"round_id"`
	Number   int       `json:"round_number"`
	OpensAt  time.Time `json:"opens_at"`
	ClosesAt time.Time `json:"closes_at"`
}

// BetAcceptedPayload is the bet.place acknowledgement. It carries no balance
// by design — the spec requires the balance to arrive only at round end.
type BetAcceptedPayload struct {
	BetID   string `json:"bet_id"`
	RoomID  string `json:"room_id"`
	RoundID string `json:"round_id"`
	Number  int    `json:"round_number"`
	Choice  string `json:"choice"`
	Amount  int64  `json:"amount"`
}

// WinnerPayload is one winning bet inside a round result.
type WinnerPayload struct {
	PlayerID string `json:"player_id"`
	BetID    string `json:"bet_id"`
	Choice   string `json:"choice"`
	Amount   int64  `json:"amount"`
	Payout   int64  `json:"payout"`
}

// RoundResultPayload is the round.result event body.
type RoundResultPayload struct {
	RoomID     string          `json:"room_id"`
	RoundID    string          `json:"round_id"`
	Number     int             `json:"round_number"`
	Die1       int             `json:"die1"`
	Die2       int             `json:"die2"`
	Sum        int             `json:"sum"`
	Outcome    string          `json:"outcome"`
	Winners    []WinnerPayload `json:"winners"`
	RoomClosed bool            `json:"room_closed"`
}

// BalanceUpdatedPayload is the balance.updated event body. Reason is
// "round_end" or "room_end".
type BalanceUpdatedPayload struct {
	PlayerID string `json:"player_id"`
	RoomID   string `json:"room_id"`
	Balance  int64  `json:"balance"`
	Reason   string `json:"reason"`
}

// RoomClosedPayload is the room.closed event body.
type RoomClosedPayload struct {
	RoomID string `json:"room_id"`
	Reason string `json:"reason"`
}

// ErrorPayload is the error event body. Event echoes the command that failed,
// so a client can correlate it without a request id.
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Event   string `json:"event,omitempty"`
}
