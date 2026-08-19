package ws

import (
	"errors"

	gameapp "github.com/mateusxis/cassino/internal/application/game"
	"github.com/mateusxis/cassino/internal/domain/game"
	"github.com/mateusxis/cassino/internal/domain/player"
)

// Transport-level errors raised by this package.
var (
	// errUnknownEvent is returned for a message type the protocol does not
	// define.
	errUnknownEvent = errors.New("ws: unknown event")
	// errMalformedMessage is returned when a frame is not the expected JSON.
	errMalformedMessage = errors.New("ws: malformed message")
	// errUnauthenticated is returned when the handshake carried no usable
	// token and none arrived as the first message.
	errUnauthenticated = errors.New("ws: authentication required")
	// errReplaced is sent to a socket that a newer connection for the same
	// player has just displaced.
	errReplaced = errors.New("ws: connection replaced by a newer one")
)

// mapError translates an error into the stable (code, message) pair carried by
// the error event. The codes mirror the REST envelope so a client sees the same
// vocabulary on both channels.
func mapError(err error) (code string, message string) {
	switch {
	case errors.Is(err, errUnauthenticated):
		return "unauthorized", "authentication is required"
	case errors.Is(err, errUnknownEvent):
		return "unknown_event", "unsupported event type"
	case errors.Is(err, errMalformedMessage):
		return "invalid_request", "the message is malformed"
	case errors.Is(err, errReplaced):
		return "connection_replaced", "this connection was replaced by a newer one"

	case errors.Is(err, gameapp.ErrNotInRoom):
		return "not_in_room", "join a room first"
	case errors.Is(err, gameapp.ErrJoinInProgress):
		return "join_in_progress", "another join is in progress, retry"
	case errors.Is(err, gameapp.ErrEngineStopped):
		return "server_shutting_down", "the server is shutting down"

	case errors.Is(err, game.ErrRoomNotFound):
		return "room_not_found", "room not found"
	case errors.Is(err, game.ErrRoundNotFound), errors.Is(err, game.ErrBetNotFound):
		return "not_found", "resource not found"
	case errors.Is(err, game.ErrRoomFull):
		return "room_full", "the room is full"
	case errors.Is(err, game.ErrRoomClosed):
		return "room_closed", "the room is closed"
	case errors.Is(err, game.ErrRoomNotOpen):
		return "room_not_open", "the room is no longer accepting players"
	case errors.Is(err, game.ErrAlreadyInAnotherRoom):
		return "already_in_another_room", "you are already in another room"
	case errors.Is(err, game.ErrNotAMember):
		return "not_a_member", "you are not a member of this room"
	case errors.Is(err, game.ErrNotOwner):
		return "not_owner", "only the room owner may do that"
	case errors.Is(err, game.ErrRoundInProgress):
		return "round_in_progress", "a round is already in progress"
	case errors.Is(err, game.ErrNoActiveRound):
		return "no_active_round", "there is no round accepting bets"
	case errors.Is(err, game.ErrRoundAlreadySettled):
		return "round_settled", "the round is already settled"
	case errors.Is(err, game.ErrMaxRoundsReached):
		return "max_rounds_reached", "the room played all of its rounds"
	case errors.Is(err, game.ErrNoPlayers):
		return "no_players", "the room has no players"
	case errors.Is(err, game.ErrBettingClosed), errors.Is(err, game.ErrBettingNotOpen):
		return "betting_closed", "the betting window is closed"
	case errors.Is(err, game.ErrDuplicateBet):
		return "duplicate_bet", "you already placed a bet in this round"
	case errors.Is(err, game.ErrInvalidChoice):
		return "invalid_request", "choice must be even or odd"
	case errors.Is(err, game.ErrInvalidAmount), errors.Is(err, player.ErrInvalidAmount):
		return "invalid_request", "amount must be greater than zero"
	case errors.Is(err, game.ErrInvalidID):
		return "invalid_request", "invalid identifier"

	case errors.Is(err, player.ErrInsufficientBalance):
		return "insufficient_balance", "balance is insufficient for this bet"
	case errors.Is(err, player.ErrNotFound):
		return "not_found", "resource not found"

	default:
		return "internal_error", "an unexpected error occurred"
	}
}
