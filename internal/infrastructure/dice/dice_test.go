package dice_test

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mateusxis/cassino/internal/domain/game"
	"github.com/mateusxis/cassino/internal/infrastructure/dice"
)

func baseTestTime() time.Time {
	return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
}

func TestRollStaysInRange(t *testing.T) {
	t.Parallel()

	roller := dice.NewCryptoRoller()
	for i := 0; i < 10_000; i++ {
		d1, d2, err := roller.Roll()
		require.NoError(t, err)
		require.GreaterOrEqual(t, d1, 1)
		require.LessOrEqual(t, d1, 6)
		require.GreaterOrEqual(t, d2, 1)
		require.LessOrEqual(t, d2, 6)

		_, err = game.OutcomeOf(d1, d2)
		require.NoError(t, err)
	}
}

// Each face must appear roughly one sixth of the time. A modulo-biased
// generator would show up here, and any face skew would move the parity split
// away from 50/50 and therefore the realised RTP away from 0.96.
func TestFaceDistributionIsUniform(t *testing.T) {
	t.Parallel()

	const rolls = 60_000
	roller := dice.NewCryptoRoller()

	counts := make([]int, 7)
	for i := 0; i < rolls; i++ {
		d1, d2, err := roller.Roll()
		require.NoError(t, err)
		counts[d1]++
		counts[d2]++
	}

	samples := float64(rolls * 2)
	expected := samples / 6
	// A 3% tolerance is far outside the sampling noise for 120k samples
	// (sigma is about 0.35% of the expectation) but tight enough to catch a
	// genuinely biased source.
	for face := 1; face <= 6; face++ {
		deviation := math.Abs(float64(counts[face])-expected) / expected
		assert.Less(t, deviation, 0.03, "face %d appeared %d times, expected about %.0f", face, counts[face], expected)
	}
}

// The headline property of the whole engine: betting through the real
// crypto/rand roller returns 0.96 of turnover on average.
//
// The bound is asserted on the sample mean over 100k rounds. With p=0.5 and a
// per-round return of either 0 or 1.92, the standard deviation of the mean is
// 0.96/sqrt(100000) ~= 0.003, so the [0.94, 0.98] window is about 6.6 sigma
// wide on each side — the test is stable, and a payout-policy regression of
// even 1% would blow straight through it.
func TestRealizedRTPIsCloseToTarget(t *testing.T) {
	t.Parallel()

	const (
		rounds = 100_000
		stake  = int64(10_000) // 100.00 currency units, chosen so 1.92x is exact
	)

	roller := dice.NewCryptoRoller()

	var wagered, returned int64
	var wins int
	for i := 0; i < rounds; i++ {
		// Alternate the backed side so a hypothetical parity bias in the
		// source cannot be masked by always betting the same way.
		choice := game.ChoiceEven
		if i%2 == 1 {
			choice = game.ChoiceOdd
		}

		d1, d2, err := roller.Roll()
		require.NoError(t, err)
		outcome, err := game.OutcomeOf(d1, d2)
		require.NoError(t, err)

		wagered += stake
		if choice.Matches(outcome) {
			wins++
			returned += game.Payout(stake)
		}
	}

	rtp := float64(returned) / float64(wagered)
	t.Logf("rounds=%d wins=%d win_rate=%.4f rtp=%.4f", rounds, wins, float64(wins)/float64(rounds), rtp)

	assert.GreaterOrEqual(t, rtp, 0.94, "realised RTP fell below the acceptable band")
	assert.LessOrEqual(t, rtp, 0.98, "realised RTP rose above the acceptable band")
	// The win rate itself must sit near 0.5; a drift here would point at the
	// entropy source rather than the payout policy.
	assert.InDelta(t, 0.5, float64(wins)/float64(rounds), 0.02)
}

// A losing bet returns nothing and a winning one returns 1.92x, so simulating
// the full room flow must land on the same RTP as the shortcut above.
func TestRTPThroughRoundSettlement(t *testing.T) {
	t.Parallel()

	const (
		// 10k rooms x 10 rounds = 100k settlements, so the sample mean has the
		// same ~0.003 standard deviation as the test above and the [0.94, 0.98]
		// band stays many sigma wide.
		rooms  = 10_000
		stake  = int64(10_000)
		window = game.DefaultBettingWindow
	)

	roller := dice.NewCryptoRoller()
	base := baseTestTime()

	var wagered, returned int64
	for i := 0; i < rooms; i++ {
		room, err := game.NewRoom("room", "player", base)
		require.NoError(t, err)

		for n := 1; n <= game.MaxRounds; n++ {
			round, err := room.StartRound("round", base, window)
			require.NoError(t, err)

			choice := game.ChoiceEven
			if (i+n)%2 == 1 {
				choice = game.ChoiceOdd
			}
			bet, err := game.NewBet("bet", round.ID, "player", choice, stake, base)
			require.NoError(t, err)
			require.NoError(t, room.PlaceBet(bet, base))
			wagered += stake

			results, err := room.SettleRound(roller, base.Add(window))
			require.NoError(t, err)
			require.Len(t, results, 1)
			returned += results[0].Payout
		}

		// Ten rounds auto-close the room.
		require.Equal(t, game.RoomClosed, room.Status)
	}

	rtp := float64(returned) / float64(wagered)
	t.Logf("rooms=%d rounds=%d rtp=%.4f", rooms, rooms*game.MaxRounds, rtp)
	assert.GreaterOrEqual(t, rtp, 0.94)
	assert.LessOrEqual(t, rtp, 0.98)
}
