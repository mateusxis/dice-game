package game

import (
	"context"
	"errors"
	"log/slog"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/game"
)

// RecoverRoomsUseCase cleans up after a process that died mid-table.
//
// Room state lives in two places: PostgreSQL rows, which survive a restart,
// and a per-room goroutine holding the betting timer, which does not. A room
// still marked open or closing at boot therefore has nobody left to settle its
// round, and its members are still bound to it in Redis — which would block
// their withdrawals forever.
//
// The v1 policy is deliberately blunt and safe: **close every non-closed room
// at startup and refund any open stakes** (see AbortRoomUseCase). Nothing is
// resumed. Resuming would mean re-arming timers whose windows have already
// expired in wall-clock terms, and settling bets placed against a window the
// other players never saw the end of.
//
// A production system would want the finer-grained alternative — persist the
// engine's intent, resume rooms whose window has not expired and settle those
// whose window has — which is exactly the trade-off worth recording as an ADR.
type RecoverRoomsUseCase struct {
	rooms  ports.RoomRecoveryRepository
	abort  *AbortRoomUseCase
	logger *slog.Logger
}

// NewRecoverRoomsUseCase wires the startup recovery pass.
func NewRecoverRoomsUseCase(rooms ports.RoomRecoveryRepository, abort *AbortRoomUseCase, logger *slog.Logger) *RecoverRoomsUseCase {
	if logger == nil {
		logger = slog.Default()
	}
	return &RecoverRoomsUseCase{rooms: rooms, abort: abort, logger: logger}
}

// RecoverRoomsOutput reports what the pass did.
type RecoverRoomsOutput struct {
	RoomsFound  int
	RoomsClosed int
	Failures    int
}

// Execute closes every room a previous process left behind. A failure on one
// room is logged and does not stop the others: booting the API matters more
// than any single stale table.
func (uc *RecoverRoomsUseCase) Execute(ctx context.Context) (RecoverRoomsOutput, error) {
	ids, err := uc.rooms.ListActiveRoomIDs(ctx)
	if err != nil {
		return RecoverRoomsOutput{}, err
	}

	out := RecoverRoomsOutput{RoomsFound: len(ids)}
	for _, roomID := range ids {
		closed, err := uc.abort.Execute(ctx, AbortRoomInput{RoomID: roomID, Reason: ReasonRecovered})
		if err != nil {
			if errors.Is(err, game.ErrRoomClosed) {
				continue
			}
			out.Failures++
			uc.logger.Error("recovery: failed to close stale room", "room_id", roomID, "error", err)
			continue
		}
		out.RoomsClosed++
		uc.logger.Info("recovery: stale room closed",
			"room_id", closed.RoomID, "members", len(closed.Members), "reason", closed.Reason)
	}
	return out, nil
}
