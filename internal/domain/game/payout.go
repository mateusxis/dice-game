package game

// Payout policy for the even/odd bet on the sum of two fair dice.
//
// RTP rationale
// -------------
// Two fair six-sided dice produce 36 equally likely ordered outcomes. Exactly
// 18 of them sum to an even number and 18 to an odd number, so an even/odd bet
// is a true 50/50 proposition — there is no "house zero" in the dice
// themselves. The house edge therefore has to live entirely in the payout
// multiplier.
//
// A fair (0% edge) game would pay 2.00x the stake on a win. The product spec
// requires a Return To Player of 0.96, i.e. the player gets back 96% of what
// they wager over the long run:
//
//	RTP = P(win) * multiplier = 0.5 * multiplier
//	0.96 = 0.5 * multiplier  =>  multiplier = 1.92
//
// So a winning bet returns 1.92x the stake (stake + 0.92x profit) and a losing
// bet returns nothing, giving a 4% house edge.
//
// Integer math
// ------------
// Money is int64 cents; floats are never used. The multiplier is applied as
//
//	payout = stake * 192 / 100
//
// evaluated left to right, so the multiplication happens at full precision and
// the single integer division truncates toward zero at the very end. Truncation
// means fractions of a cent are kept by the house, which nudges the realised
// RTP infinitesimally below 0.96 for odd stakes (e.g. a stake of 1 cent pays
// 1 cent, a stake of 3 cents pays 5 cents instead of 5.76). This is the
// conventional, auditable direction to round in a betting system: it can never
// mint money out of nothing.
//
// Overflow: stake * 192 stays within int64 for any stake below ~4.8e16 cents
// (~480 trillion currency units), far above any balance this system can hold.
const (
	// PayoutNumerator and PayoutDenominator express the 1.92x win multiplier
	// as an exact rational so the computation stays in integer arithmetic.
	PayoutNumerator   int64 = 192
	PayoutDenominator int64 = 100

	// TargetRTP is the designed long-run return to player (documentation only;
	// the authoritative value is the numerator/denominator pair above).
	TargetRTP = 0.96
)

// Payout returns the gross amount (in cents) credited to a player for a
// winning stake. It returns 0 for non-positive stakes.
//
// The result is the *gross* return: it already includes the stake, which was
// debited when the bet was placed. Net profit is Payout(stake) - stake.
func Payout(stake int64) int64 {
	if stake <= 0 {
		return 0
	}
	return stake * PayoutNumerator / PayoutDenominator
}
