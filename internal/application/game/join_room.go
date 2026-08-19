package game

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/game"
)

// JoinRoomUseCase seats a player at an existing table. Joining happens over
// WebSocket (room.join), so this use case is called from the socket handler.
type JoinRoomUseCase struct {
	tx       ports.TxManager
	rooms    ports.RoomRepository
	state    ports.RoomStateStore
	sessions ports.PlayerSessionStore
	clock    ports.Clock
	// sleep is time.Sleep in production; tests replace it so the seat-mutex
	// retry loop costs nothing.
	sleep func(time.Duration)
}

// NewJoinRoomUseCase wires a JoinRoomUseCase from its ports.
func NewJoinRoomUseCase(
	tx ports.TxManager,
	rooms ports.RoomRepository,
	state ports.RoomStateStore,
	sessions ports.PlayerSessionStore,
	clock ports.Clock,
) *JoinRoomUseCase {
	return &JoinRoomUseCase{tx: tx, rooms: rooms, state: state, sessions: sessions, clock: clock, sleep: time.Sleep}
}

// Execute seats the player, or reports why it could not.
//
// Three independent guards protect the two rules that matter:
//
//	one room per player  -> Redis SET NX binding, then the authoritative
//	                        FindActiveRoomForPlayer check inside the tx;
//	at most six players   -> the Redis seat mutex around the read-modify-write,
//	                        the aggregate's own Full() check on a row-locked
//	                        room, and finally the room_players primary key.
//
// Joining is idempotent: a player already seated in the room (the owner
// attaching over WebSocket after creating the room over REST, or a client
// reconnecting) gets a successful response with AlreadyMember set, never an
// error. Everything else — a full room, a closing room, membership of another
// room — is a domain error.
func (uc *JoinRoomUseCase) Execute(ctx context.Context, in JoinRoomInput) (JoinRoomOutput, error) {
	if strings.TrimSpace(in.RoomID) == "" || strings.TrimSpace(in.PlayerID) == "" {
		return JoinRoomOutput{}, game.ErrInvalidID
	}

	current, err := uc.sessions.ActiveRoom(ctx, in.PlayerID)
	if err != nil {
		return JoinRoomOutput{}, err
	}
	if current != "" && current != in.RoomID {
		return JoinRoomOutput{}, game.ErrAlreadyInAnotherRoom
	}
	if current == in.RoomID {
		// Already claimed for this room. If the seat row exists too, this is a
		// re-attach and there is nothing to do; if it does not, the binding is
		// stale and the join proceeds normally to create the missing row.
		room, err := uc.rooms.GetByID(ctx, in.RoomID)
		if err != nil {
			return JoinRoomOutput{}, err
		}
		if room.HasPlayer(in.PlayerID) {
			return JoinRoomOutput{Room: summaryOf(room), Players: room.Players(), AlreadyMember: true}, nil
		}
	}

	release, err := uc.acquireSeatMutex(ctx, in.RoomID)
	if err != nil {
		return JoinRoomOutput{}, err
	}
	defer func() { _ = release(context.WithoutCancel(ctx)) }()

	// claimedHere records whether *this* call created the session binding, so a
	// later failure only releases what it took.
	claimedHere := false
	if current != in.RoomID {
		bound, err := uc.sessions.BindPlayerToRoom(ctx, in.PlayerID, in.RoomID, sessionTTL)
		if err != nil {
			return JoinRoomOutput{}, err
		}
		if !bound {
			// Someone bound this player between our read and our write.
			where, err := uc.sessions.ActiveRoom(ctx, in.PlayerID)
			if err != nil {
				return JoinRoomOutput{}, err
			}
			if where != in.RoomID {
				return JoinRoomOutput{}, game.ErrAlreadyInAnotherRoom
			}
		} else {
			claimedHere = true
		}
	}

	var (
		out           JoinRoomOutput
		alreadySeated bool
	)
	err = uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		room, err := uc.rooms.GetForUpdate(ctx, in.RoomID)
		if err != nil {
			return err
		}
		existing, err := uc.rooms.FindActiveRoomForPlayer(ctx, in.PlayerID)
		if err != nil {
			return err
		}
		if existing != "" && existing != in.RoomID {
			return game.ErrAlreadyInAnotherRoom
		}

		now := uc.clock.Now()
		if err := room.Join(in.PlayerID, now); err != nil {
			if errors.Is(err, game.ErrAlreadyJoined) {
				alreadySeated = true
				out = JoinRoomOutput{Room: summaryOf(room), Players: room.Players(), AlreadyMember: true}
				return nil
			}
			return err
		}
		if err := uc.rooms.AddPlayer(ctx, in.RoomID, in.PlayerID, now); err != nil {
			if errors.Is(err, game.ErrAlreadyJoined) {
				alreadySeated = true
				out = JoinRoomOutput{Room: summaryOf(room), Players: room.Players(), AlreadyMember: true}
				return nil
			}
			return err
		}
		out = JoinRoomOutput{Room: summaryOf(room), Players: room.Players()}
		return nil
	})
	if err != nil {
		if claimedHere {
			_ = uc.sessions.ReleasePlayer(context.WithoutCancel(ctx), in.PlayerID)
		}
		return JoinRoomOutput{}, err
	}
	if !alreadySeated {
		// Only a real seat change moves the player count, so only then is the
		// cached summary stale. A cache write failure is not fatal: PostgreSQL
		// remains the source of truth for the listing.
		_ = uc.state.SaveRoom(ctx, out.Room, roomCacheTTL)
	}
	return out, nil
}

// acquireSeatMutex takes the per-room join lock, retrying briefly: contention
// here is measured in milliseconds, so a short backoff turns a benign race
// into a slightly slower join rather than a failure.
func (uc *JoinRoomUseCase) acquireSeatMutex(ctx context.Context, roomID string) (func(context.Context) error, error) {
	for attempt := 0; attempt < joinLockAttempts; attempt++ {
		release, acquired, err := uc.state.AcquireJoinLock(ctx, roomID, joinLockTTL)
		if err != nil {
			return nil, err
		}
		if acquired {
			return release, nil
		}
		if attempt < joinLockAttempts-1 {
			uc.sleep(joinLockBackoff)
		}
	}
	return nil, ErrJoinInProgress
}
