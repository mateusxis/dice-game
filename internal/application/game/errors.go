package game

import (
	"errors"

	"github.com/mateusxis/cassino/internal/domain/game"
)

// Application-level errors. Everything else these use cases return is a domain
// error from internal/domain/game, which the delivery layers already map.
var (
	// ErrJoinInProgress is returned when the per-room seat mutex stays held by
	// another join for longer than this use case is willing to wait. It is a
	// "try again" condition, not a rejection.
	ErrJoinInProgress = errors.New("game: another join is in progress for this room")
	// ErrEngineStopped is returned when a command reaches the round engine
	// after it has been shut down.
	ErrEngineStopped = errors.New("game: round engine is shutting down")
	// ErrNotInRoom is returned when a player acts on a room they are not
	// currently seated in.
	ErrNotInRoom = errors.New("game: player is not in a room")
)

// isRoundMissing reports whether err means "this room has no round yet",
// which several use cases treat as a normal state rather than a failure.
func isRoundMissing(err error) bool {
	return errors.Is(err, game.ErrRoundNotFound)
}
