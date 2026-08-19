package game

import (
	"strings"
	"time"
)

// Choice is the side a player backs: even or odd.
type Choice string

const (
	// ChoiceEven backs an even dice sum.
	ChoiceEven Choice = "even"
	// ChoiceOdd backs an odd dice sum.
	ChoiceOdd Choice = "odd"
)

// Valid reports whether c is a known choice.
func (c Choice) Valid() bool {
	return c == ChoiceEven || c == ChoiceOdd
}

// String implements fmt.Stringer.
func (c Choice) String() string { return string(c) }

// ParseChoice normalizes free-form input into a Choice.
func ParseChoice(s string) (Choice, error) {
	c := Choice(strings.ToLower(strings.TrimSpace(s)))
	if !c.Valid() {
		return "", ErrInvalidChoice
	}
	return c, nil
}

// Matches reports whether the choice wins against an outcome.
func (c Choice) Matches(o Outcome) bool {
	return string(c) == string(o)
}

// Bet is a single wager placed by one player in one round. The (RoundID,
// PlayerID) pair is unique — enforced both here (Round.PlaceBet) and by a
// database unique constraint, which is what actually defeats concurrent
// duplicate submissions.
type Bet struct {
	ID       string
	RoundID  string
	PlayerID string
	Choice   Choice
	// Amount is the stake in cents. It is debited from the player's balance
	// the moment the bet is accepted.
	Amount int64
	// Won and Payout are nil until the round is settled.
	Won    *bool
	Payout *int64

	PlacedAt time.Time
}

// NewBet builds a validated, unsettled bet.
func NewBet(id, roundID, playerID string, choice Choice, amount int64, placedAt time.Time) (*Bet, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(roundID) == "" || strings.TrimSpace(playerID) == "" {
		return nil, ErrInvalidID
	}
	if !choice.Valid() {
		return nil, ErrInvalidChoice
	}
	if amount <= 0 {
		return nil, ErrInvalidAmount
	}
	return &Bet{
		ID:       id,
		RoundID:  roundID,
		PlayerID: playerID,
		Choice:   choice,
		Amount:   amount,
		PlacedAt: placedAt.UTC(),
	}, nil
}

// Settled reports whether the bet already carries a result.
func (b *Bet) Settled() bool { return b.Won != nil }

// Settle resolves the bet against a round outcome, recording whether it won
// and how much is credited back to the player. A losing bet records a payout
// of zero (the stake was already debited at placement time).
func (b *Bet) Settle(outcome Outcome) error {
	if !outcome.Valid() {
		return ErrInvalidChoice
	}
	if b.Settled() {
		return ErrRoundAlreadySettled
	}
	won := b.Choice.Matches(outcome)
	var payout int64
	if won {
		payout = Payout(b.Amount)
	}
	b.Won = &won
	b.Payout = &payout
	return nil
}

// Result is a read-only view of a settled bet, convenient for transport
// layers that must not expose internal pointers.
type Result struct {
	BetID    string
	PlayerID string
	Choice   Choice
	Amount   int64
	Won      bool
	Payout   int64
}

// Result returns the settled view of the bet. The second return value is
// false when the bet has not been settled yet.
func (b *Bet) Result() (Result, bool) {
	if !b.Settled() {
		return Result{}, false
	}
	return Result{
		BetID:    b.ID,
		PlayerID: b.PlayerID,
		Choice:   b.Choice,
		Amount:   b.Amount,
		Won:      *b.Won,
		Payout:   *b.Payout,
	}, true
}
