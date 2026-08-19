package game_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mateusxis/cassino/internal/domain/game"
)

func TestPayoutMultiplier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		stake int64
		want  int64
	}{
		{"one currency unit", 100, 192},
		{"ten units", 1000, 1920},
		{"large round stake", 1_000_000, 1_920_000},
		// Odd stakes truncate toward zero: the fraction of a cent stays with
		// the house, which can never mint money out of nothing.
		{"one cent truncates 1.92 to 1", 1, 1},
		{"three cents truncates 5.76 to 5", 3, 5},
		{"seven cents truncates 13.44 to 13", 7, 13},
		{"twenty five cents is exact", 25, 48},
		{"ninety nine cents truncates 190.08 to 190", 99, 190},
		{"one cent above a unit", 101, 193},
		{"non positive stakes pay nothing", 0, 0},
		{"negative stakes pay nothing", -100, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, game.Payout(tc.stake))
		})
	}
}

// Payout is the gross return, so a win always leaves the player better off
// than before the bet — except for sub-cent stakes where truncation bites.
func TestPayoutNeverExceedsFairDouble(t *testing.T) {
	t.Parallel()

	for stake := int64(1); stake <= 10_000; stake++ {
		payout := game.Payout(stake)
		require.LessOrEqual(t, payout, 2*stake, "payout must stay below a fair 2x for stake %d", stake)
		require.GreaterOrEqual(t, payout, stake, "a winning bet must return at least the stake for %d", stake)
		// Truncation can never cost more than one cent versus exact 1.92x.
		exactTimes100 := stake * game.PayoutNumerator
		require.LessOrEqual(t, exactTimes100-payout*game.PayoutDenominator, game.PayoutDenominator-1,
			"truncation loss must stay under one cent for stake %d", stake)
	}
}

// The designed house edge: over an even split of wins and losses the operator
// keeps 4% of turnover.
func TestPayoutImpliesFourPercentHouseEdge(t *testing.T) {
	t.Parallel()

	const stake int64 = 1_000_00 // 1000.00 currency units in cents
	// One win and one loss over two identical bets.
	turnover := 2 * stake
	returned := game.Payout(stake)

	rtp := float64(returned) / float64(turnover)
	assert.InDelta(t, game.TargetRTP, rtp, 1e-9)
}

func TestOutcomeOf(t *testing.T) {
	t.Parallel()

	even, err := game.OutcomeOf(1, 3)
	require.NoError(t, err)
	assert.Equal(t, game.OutcomeEven, even)

	odd, err := game.OutcomeOf(1, 2)
	require.NoError(t, err)
	assert.Equal(t, game.OutcomeOdd, odd)

	for _, dice := range [][2]int{{0, 3}, {7, 1}, {3, 0}, {-1, 2}} {
		_, err := game.OutcomeOf(dice[0], dice[1])
		assert.ErrorIs(t, err, game.ErrInvalidDie, "dice %v must be rejected", dice)
	}
}

// Every one of the 36 ordered dice combinations must resolve, and the parity
// split must be exactly 18/18 — that 50/50 is what makes 1.92x mean 0.96 RTP.
func TestDiceParityIsBalanced(t *testing.T) {
	t.Parallel()

	var evens, odds int
	for d1 := 1; d1 <= 6; d1++ {
		for d2 := 1; d2 <= 6; d2++ {
			outcome, err := game.OutcomeOf(d1, d2)
			require.NoError(t, err)
			if outcome == game.OutcomeEven {
				evens++
			} else {
				odds++
			}
		}
	}
	assert.Equal(t, 18, evens)
	assert.Equal(t, 18, odds)
}

func TestParseChoice(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"even", "EVEN", " Even "} {
		c, err := game.ParseChoice(in)
		require.NoError(t, err)
		assert.Equal(t, game.ChoiceEven, c)
	}
	_, err := game.ParseChoice("maybe")
	assert.ErrorIs(t, err, game.ErrInvalidChoice)
}
