package game_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mateusxis/cassino/internal/domain/game"
)

// fixedRoller returns pre-set dice, letting tests drive settlement
// deterministically.
type fixedRoller struct {
	die1, die2 int
	err        error
}

func (r fixedRoller) Roll() (int, int, error) { return r.die1, r.die2, r.err }

func newTestRound(t *testing.T) *game.Round {
	t.Helper()
	round, err := game.NewRound("round-1", "room-1", 1, baseTime, game.DefaultBettingWindow)
	require.NoError(t, err)
	return round
}

func TestNewRoundValidation(t *testing.T) {
	t.Parallel()

	round := newTestRound(t)
	assert.Equal(t, game.RoundBetting, round.Status)
	assert.Equal(t, baseTime, round.BettingOpensAt)
	assert.Equal(t, baseTime.Add(15*time.Second), round.BettingClosesAt)

	_, err := game.NewRound("", "room-1", 1, baseTime, game.DefaultBettingWindow)
	assert.ErrorIs(t, err, game.ErrInvalidID)

	_, err = game.NewRound("round-1", "room-1", 0, baseTime, game.DefaultBettingWindow)
	assert.ErrorIs(t, err, game.ErrInvalidRoundNumber)

	_, err = game.NewRound("round-1", "room-1", game.MaxRounds+1, baseTime, game.DefaultBettingWindow)
	assert.ErrorIs(t, err, game.ErrInvalidRoundNumber)

	_, err = game.NewRound("round-1", "room-1", 1, baseTime, -time.Second)
	assert.ErrorIs(t, err, game.ErrInvalidBettingWindow)
}

// The window is half-open [opens, closes): a bet at the exact closing instant
// is already too late. The backend clock, not the client, decides.
func TestBettingWindowBoundaries(t *testing.T) {
	t.Parallel()

	round := newTestRound(t)
	closesAt := baseTime.Add(game.DefaultBettingWindow)

	tests := []struct {
		name string
		at   time.Time
		open bool
	}{
		{"one second before opening", baseTime.Add(-time.Second), false},
		{"exactly at opening", baseTime, true},
		{"mid window", baseTime.Add(7 * time.Second), true},
		{"one nanosecond before closing", closesAt.Add(-time.Nanosecond), true},
		{"exactly at closing", closesAt, false},
		{"after closing", closesAt.Add(time.Nanosecond), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.open, round.BettingOpen(tc.at))
		})
	}
}

func TestRoundPlaceBetWindowEnforcement(t *testing.T) {
	t.Parallel()

	t.Run("accepts inside the window", func(t *testing.T) {
		t.Parallel()
		round := newTestRound(t)
		bet, err := game.NewBet("bet-1", "round-1", "alice", game.ChoiceEven, 500, baseTime)
		require.NoError(t, err)

		require.NoError(t, round.PlaceBet(bet, baseTime.Add(5*time.Second)))
		assert.True(t, round.HasBet("alice"))
		assert.Len(t, round.Bets(), 1)
	})

	t.Run("rejects before the window opens", func(t *testing.T) {
		t.Parallel()
		round := newTestRound(t)
		bet, err := game.NewBet("bet-1", "round-1", "alice", game.ChoiceEven, 500, baseTime)
		require.NoError(t, err)

		assert.ErrorIs(t, round.PlaceBet(bet, baseTime.Add(-time.Millisecond)), game.ErrBettingNotOpen)
		assert.False(t, round.HasBet("alice"))
	})

	t.Run("rejects at and after the closing instant", func(t *testing.T) {
		t.Parallel()
		round := newTestRound(t)
		bet, err := game.NewBet("bet-1", "round-1", "alice", game.ChoiceEven, 500, baseTime)
		require.NoError(t, err)

		closesAt := baseTime.Add(game.DefaultBettingWindow)
		assert.ErrorIs(t, round.PlaceBet(bet, closesAt), game.ErrBettingClosed)
		assert.ErrorIs(t, round.PlaceBet(bet, closesAt.Add(time.Hour)), game.ErrBettingClosed)
		assert.False(t, round.HasBet("alice"))
	})

	t.Run("rejects a bet belonging to another round", func(t *testing.T) {
		t.Parallel()
		round := newTestRound(t)
		bet, err := game.NewBet("bet-1", "other-round", "alice", game.ChoiceEven, 500, baseTime)
		require.NoError(t, err)
		assert.ErrorIs(t, round.PlaceBet(bet, baseTime), game.ErrInvalidID)
	})
}

func TestRoundRejectsDuplicateBetPerPlayer(t *testing.T) {
	t.Parallel()

	round := newTestRound(t)
	first, err := game.NewBet("bet-1", "round-1", "alice", game.ChoiceEven, 500, baseTime)
	require.NoError(t, err)
	require.NoError(t, round.PlaceBet(first, baseTime))

	second, err := game.NewBet("bet-2", "round-1", "alice", game.ChoiceOdd, 100, baseTime)
	require.NoError(t, err)
	assert.ErrorIs(t, round.PlaceBet(second, baseTime.Add(time.Second)), game.ErrDuplicateBet)
	assert.Len(t, round.Bets(), 1)
}

func TestNewBetValidation(t *testing.T) {
	t.Parallel()

	_, err := game.NewBet("", "round-1", "alice", game.ChoiceEven, 100, baseTime)
	assert.ErrorIs(t, err, game.ErrInvalidID)

	_, err = game.NewBet("bet-1", "round-1", "alice", game.Choice("maybe"), 100, baseTime)
	assert.ErrorIs(t, err, game.ErrInvalidChoice)

	for _, amount := range []int64{0, -1} {
		_, err = game.NewBet("bet-1", "round-1", "alice", game.ChoiceEven, amount, baseTime)
		assert.ErrorIs(t, err, game.ErrInvalidAmount)
	}
}

func TestRoundSettleResolvesEveryBet(t *testing.T) {
	t.Parallel()

	round := newTestRound(t)

	evenBet, err := game.NewBet("bet-even", "round-1", "alice", game.ChoiceEven, 1000, baseTime)
	require.NoError(t, err)
	require.NoError(t, round.PlaceBet(evenBet, baseTime))

	oddBet, err := game.NewBet("bet-odd", "round-1", "bob", game.ChoiceOdd, 300, baseTime.Add(time.Second))
	require.NoError(t, err)
	require.NoError(t, round.PlaceBet(oddBet, baseTime.Add(time.Second)))

	settledAt := baseTime.Add(game.DefaultBettingWindow)
	// 3 + 3 = 6, an even sum: the even bet wins, the odd bet loses.
	results, err := round.Settle(fixedRoller{die1: 3, die2: 3}, settledAt)
	require.NoError(t, err)
	require.Len(t, results, 2)

	assert.Equal(t, game.RoundSettled, round.Status)
	assert.Equal(t, game.OutcomeEven, round.Outcome)
	assert.Equal(t, 3, round.Die1)
	assert.Equal(t, 3, round.Die2)
	require.NotNil(t, round.SettledAt)
	assert.Equal(t, settledAt, *round.SettledAt)

	assert.Equal(t, "alice", results[0].PlayerID)
	assert.True(t, results[0].Won)
	assert.Equal(t, int64(1920), results[0].Payout)

	assert.Equal(t, "bob", results[1].PlayerID)
	assert.False(t, results[1].Won)
	assert.Zero(t, results[1].Payout)

	// Settling twice is refused, so a retry cannot re-credit winners.
	_, err = round.Settle(fixedRoller{die1: 1, die2: 2}, settledAt)
	assert.ErrorIs(t, err, game.ErrRoundAlreadySettled)
}

func TestRoundSettleRejectsBetsAfterwards(t *testing.T) {
	t.Parallel()

	round := newTestRound(t)
	_, err := round.Settle(fixedRoller{die1: 1, die2: 1}, baseTime.Add(game.DefaultBettingWindow))
	require.NoError(t, err)

	bet, err := game.NewBet("bet-1", "round-1", "alice", game.ChoiceEven, 100, baseTime)
	require.NoError(t, err)
	assert.ErrorIs(t, round.PlaceBet(bet, baseTime), game.ErrRoundAlreadySettled)
	assert.False(t, round.BettingOpen(baseTime))
}

// An entropy failure must abort settlement rather than invent a result.
func TestRoundSettlePropagatesRollerFailure(t *testing.T) {
	t.Parallel()

	round := newTestRound(t)
	boom := errors.New("entropy source unavailable")

	_, err := round.Settle(fixedRoller{err: boom}, baseTime.Add(game.DefaultBettingWindow))
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, game.RoundBetting, round.Status)
}

func TestRoundSettleRejectsInvalidDice(t *testing.T) {
	t.Parallel()

	round := newTestRound(t)
	_, err := round.Settle(fixedRoller{die1: 0, die2: 9}, baseTime)
	assert.ErrorIs(t, err, game.ErrInvalidDie)
	assert.Equal(t, game.RoundBetting, round.Status)
}

func TestBetResultUnavailableUntilSettled(t *testing.T) {
	t.Parallel()

	bet, err := game.NewBet("bet-1", "round-1", "alice", game.ChoiceEven, 250, baseTime)
	require.NoError(t, err)

	_, ok := bet.Result()
	assert.False(t, ok)
	assert.False(t, bet.Settled())

	require.NoError(t, bet.Settle(game.OutcomeEven))
	result, ok := bet.Result()
	require.True(t, ok)
	assert.True(t, result.Won)
	assert.Equal(t, game.Payout(250), result.Payout)

	assert.ErrorIs(t, bet.Settle(game.OutcomeEven), game.ErrRoundAlreadySettled)
}

func TestRehydrateRound(t *testing.T) {
	t.Parallel()

	bet, err := game.NewBet("bet-1", "round-1", "alice", game.ChoiceOdd, 100, baseTime)
	require.NoError(t, err)

	round, err := game.RehydrateRound(
		"round-1", "room-1", 2,
		baseTime, baseTime.Add(game.DefaultBettingWindow),
		0, 0, "", game.RoundBetting, nil,
		[]*game.Bet{bet},
	)
	require.NoError(t, err)
	assert.True(t, round.HasBet("alice"))
	assert.Equal(t, 2, round.Number)

	_, err = game.RehydrateRound("round-1", "room-1", 1, baseTime, baseTime, 0, 0, "", game.RoundBetting, nil, nil)
	assert.ErrorIs(t, err, game.ErrInvalidBettingWindow)
}
