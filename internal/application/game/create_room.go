package game

import (
	"context"
	"strings"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/game"
)

// CreateRoomUseCase opens a table. Any authenticated player may create one;
// they become its owner and its first seated member.
type CreateRoomUseCase struct {
	tx       ports.TxManager
	rooms    ports.RoomRepository
	state    ports.RoomStateStore
	sessions ports.PlayerSessionStore
	ids      ports.IDGenerator
	clock    ports.Clock
}

// NewCreateRoomUseCase wires a CreateRoomUseCase from its ports.
func NewCreateRoomUseCase(
	tx ports.TxManager,
	rooms ports.RoomRepository,
	state ports.RoomStateStore,
	sessions ports.PlayerSessionStore,
	ids ports.IDGenerator,
	clock ports.Clock,
) *CreateRoomUseCase {
	return &CreateRoomUseCase{tx: tx, rooms: rooms, state: state, sessions: sessions, ids: ids, clock: clock}
}

// Execute creates an open room owned by the caller.
//
// The "one room per player" rule is enforced twice, in this order:
//
//  1. the Redis session binding is claimed first with SET NX, which is atomic
//     and therefore settles a race between two simultaneous create calls from
//     the same account before either touches the database;
//  2. the authoritative PostgreSQL check runs inside the transaction, so a
//     Redis outage or an expired binding still cannot seat a player twice.
//
// If the transaction fails the binding is released again, leaving the player
// free to retry.
func (uc *CreateRoomUseCase) Execute(ctx context.Context, in CreateRoomInput) (CreateRoomOutput, error) {
	if strings.TrimSpace(in.OwnerID) == "" {
		return CreateRoomOutput{}, game.ErrInvalidID
	}

	roomID := uc.ids.NewID()
	bound, err := uc.sessions.BindPlayerToRoom(ctx, in.OwnerID, roomID, sessionTTL)
	if err != nil {
		return CreateRoomOutput{}, err
	}
	if !bound {
		return CreateRoomOutput{}, game.ErrAlreadyInAnotherRoom
	}

	room, err := game.NewRoom(roomID, in.OwnerID, uc.clock.Now())
	if err != nil {
		uc.releaseBinding(ctx, in.OwnerID)
		return CreateRoomOutput{}, err
	}

	err = uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		existing, err := uc.rooms.FindActiveRoomForPlayer(ctx, in.OwnerID)
		if err != nil {
			return err
		}
		if existing != "" {
			return game.ErrAlreadyInAnotherRoom
		}
		return uc.rooms.Create(ctx, room)
	})
	if err != nil {
		uc.releaseBinding(ctx, in.OwnerID)
		return CreateRoomOutput{}, err
	}

	summary := summaryOf(room)
	// The room is already committed; a cache write failure must not fail the
	// call. PostgreSQL is the source of truth and ListOpenRooms falls back to
	// it, so the worst case is a listing that misses this room until the next
	// cache write.
	_ = uc.state.SaveRoom(ctx, summary, roomCacheTTL)

	return CreateRoomOutput{Room: summary}, nil
}

// releaseBinding drops the session claim taken at the start of Execute. It
// uses a context detached from cancellation so a client that disconnected
// mid-call cannot leave its own account bound to a room that never existed.
func (uc *CreateRoomUseCase) releaseBinding(ctx context.Context, playerID string) {
	_ = uc.sessions.ReleasePlayer(context.WithoutCancel(ctx), playerID)
}
