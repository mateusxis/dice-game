package game

import (
	"context"
	"strings"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/game"
)

// RoomStateUseCase builds the snapshot pushed as the room.state event: who is
// seated, how far the table has got, and what the current round is doing.
type RoomStateUseCase struct {
	rooms  ports.RoomRepository
	rounds ports.RoundRepository
}

// NewRoomStateUseCase wires a RoomStateUseCase from its ports.
func NewRoomStateUseCase(rooms ports.RoomRepository, rounds ports.RoundRepository) *RoomStateUseCase {
	return &RoomStateUseCase{rooms: rooms, rounds: rounds}
}

// Execute reads a room and its most recent round straight from PostgreSQL.
// The cache is deliberately bypassed: room.state is what a client uses to
// decide whether it can still bet, so it must never be a stale projection.
func (uc *RoomStateUseCase) Execute(ctx context.Context, roomID string) (RoomState, error) {
	if strings.TrimSpace(roomID) == "" {
		return RoomState{}, game.ErrRoomNotFound
	}
	room, err := uc.rooms.GetByID(ctx, roomID)
	if err != nil {
		return RoomState{}, err
	}

	state := RoomState{Room: summaryOf(room), Players: room.Players()}

	round, err := uc.rounds.GetCurrent(ctx, roomID)
	if err != nil {
		if isRoundMissing(err) {
			return state, nil
		}
		return RoomState{}, err
	}
	state.Round = &RoundState{
		RoundID:  round.ID,
		Number:   round.Number,
		Status:   round.Status,
		OpensAt:  round.BettingOpensAt,
		ClosesAt: round.BettingClosesAt,
		Die1:     round.Die1,
		Die2:     round.Die2,
		Outcome:  round.Outcome,
	}
	return state, nil
}
