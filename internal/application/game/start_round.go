package game

import (
	"context"
	"strings"
	"time"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/game"
)

// StartRoundUseCase opens the next betting window of a room.
type StartRoundUseCase struct {
	tx     ports.TxManager
	rooms  ports.RoomRepository
	rounds ports.RoundRepository
	state  ports.RoomStateStore
	ids    ports.IDGenerator
	clock  ports.Clock
	window time.Duration
}

// NewStartRoundUseCase wires a StartRoundUseCase from its ports. window is the
// betting period length (BETTING_WINDOW, 15s by default); a non-positive value
// falls back to the domain default.
func NewStartRoundUseCase(
	tx ports.TxManager,
	rooms ports.RoomRepository,
	rounds ports.RoundRepository,
	state ports.RoomStateStore,
	ids ports.IDGenerator,
	clock ports.Clock,
	window time.Duration,
) *StartRoundUseCase {
	if window <= 0 {
		window = game.DefaultBettingWindow
	}
	return &StartRoundUseCase{tx: tx, rooms: rooms, rounds: rounds, state: state, ids: ids, clock: clock, window: window}
}

// Window returns the configured betting-window length.
func (uc *StartRoundUseCase) Window() time.Duration { return uc.window }

// Execute starts round N+1.
//
// Who may start a round: **the room owner, and only the owner**. The room has
// exactly one authority for its lifecycle — the same player who created it and
// who alone may close it — which keeps the rules simple and unambiguous when
// six players share a table. Any other member gets game.ErrNotOwner.
//
// The domain aggregate enforces the rest: the room must be open (not closing,
// not closed), no round may be live, the round cap of ten must not be reached,
// and the room must have at least one player. closes_at is computed here from
// ports.Clock — the backend clock is the only authority on the window, client
// timestamps are never read.
func (uc *StartRoundUseCase) Execute(ctx context.Context, in StartRoundInput) (StartRoundOutput, error) {
	if strings.TrimSpace(in.RoomID) == "" {
		return StartRoundOutput{}, game.ErrRoomNotFound
	}

	var (
		out     StartRoundOutput
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
		if !room.IsOwner(in.RequesterID) {
			return game.ErrNotOwner
		}

		round, err := room.StartRound(uc.ids.NewID(), uc.clock.Now(), uc.window)
		if err != nil {
			return err
		}
		if err := uc.rounds.Create(ctx, round); err != nil {
			return err
		}
		if err := uc.rooms.SetCurrentRound(ctx, room.ID, room.CurrentRound); err != nil {
			return err
		}

		summary = summaryOf(room)
		out = StartRoundOutput{
			RoomID:      room.ID,
			RoundID:     round.ID,
			RoundNumber: round.Number,
			OpensAt:     round.BettingOpensAt,
			ClosesAt:    round.BettingClosesAt,
			Members:     room.Players(),
		}
		return nil
	})
	if err != nil {
		return StartRoundOutput{}, err
	}

	_ = uc.state.SaveRoom(ctx, summary, roomCacheTTL)
	return out, nil
}
