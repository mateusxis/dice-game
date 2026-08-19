package game

import (
	"context"
	"strings"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/game"
)

// CloseRoomUseCase ends a table on the owner's request.
type CloseRoomUseCase struct {
	tx       ports.TxManager
	rooms    ports.RoomRepository
	rounds   ports.RoundRepository
	players  ports.PlayerRepository
	state    ports.RoomStateStore
	sessions ports.PlayerSessionStore
	clock    ports.Clock
}

// NewCloseRoomUseCase wires a CloseRoomUseCase from its ports.
func NewCloseRoomUseCase(
	tx ports.TxManager,
	rooms ports.RoomRepository,
	rounds ports.RoundRepository,
	players ports.PlayerRepository,
	state ports.RoomStateStore,
	sessions ports.PlayerSessionStore,
	clock ports.Clock,
) *CloseRoomUseCase {
	return &CloseRoomUseCase{tx: tx, rooms: rooms, rounds: rounds, players: players, state: state, sessions: sessions, clock: clock}
}

// Execute closes the room, gracefully.
//
// "A room is only closed when the current round ends": with a live betting
// round the room moves to "closing" and this call reports Closed=false; the
// round engine finishes that round, settles it, and the room closes as part of
// settlement. With no live round the room closes immediately.
//
// Only the owner may close a room (game.ErrNotOwner otherwise). When the room
// does close, every member is unbound from their session and the room is
// evicted from the open-room cache — which is also what unblocks their
// withdrawals.
func (uc *CloseRoomUseCase) Execute(ctx context.Context, in CloseRoomInput) (CloseRoomOutput, error) {
	if strings.TrimSpace(in.RoomID) == "" {
		return CloseRoomOutput{}, game.ErrRoomNotFound
	}
	reason := in.Reason
	if reason == "" {
		reason = ReasonOwnerClosed
	}

	var (
		out     CloseRoomOutput
		summary ports.RoomSummary
	)
	err := uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		room, err := uc.rooms.GetForUpdate(ctx, in.RoomID)
		if err != nil {
			return err
		}
		if _, err := attachLiveRound(ctx, uc.rounds, room); err != nil {
			return err
		}

		if err := room.Close(in.RequesterID, uc.clock.Now()); err != nil {
			return err
		}
		if err := uc.rooms.UpdateStatus(ctx, room.ID, room.Status, room.ClosedAt); err != nil {
			return err
		}

		summary = summaryOf(room)
		out = CloseRoomOutput{
			RoomID:  room.ID,
			Status:  room.Status,
			Closed:  room.Status == game.RoomClosed,
			Reason:  reason,
			Members: room.Players(),
		}
		if out.Closed {
			out.Balances, err = memberBalances(ctx, uc.players, out.Members)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return CloseRoomOutput{}, err
	}

	finalizeRoomCache(ctx, uc.state, uc.sessions, out.RoomID, summary, out.Closed, out.Members)
	return out, nil
}

// finalizeRoomCache keeps Redis honest after a lifecycle change: a terminal
// room is evicted and its members unbound (so their withdrawals unblock), a
// still-live room just refreshes its cached summary. Cache errors are
// swallowed — the transaction already committed and PostgreSQL is the source
// of truth — but the session release uses a cancellation-free context so a
// disconnected caller cannot leave players bound to a dead room.
func finalizeRoomCache(
	ctx context.Context,
	state ports.RoomStateStore,
	sessions ports.PlayerSessionStore,
	roomID string,
	summary ports.RoomSummary,
	closed bool,
	members []string,
) {
	if !closed {
		_ = state.SaveRoom(ctx, summary, roomCacheTTL)
		return
	}
	detached := context.WithoutCancel(ctx)
	for _, playerID := range members {
		_ = sessions.ReleasePlayer(detached, playerID)
	}
	_ = state.RemoveRoom(detached, roomID)
}
