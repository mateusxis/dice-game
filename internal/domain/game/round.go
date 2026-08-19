package game

import (
	"strings"
	"time"
)

// RoundStatus is the lifecycle state of a round.
type RoundStatus string

const (
	// RoundBetting means the betting window is (or was) open and no dice have
	// been rolled yet.
	RoundBetting RoundStatus = "betting"
	// RoundSettled means the dice were rolled and every bet has a result.
	RoundSettled RoundStatus = "settled"
)

// Valid reports whether s is a known round status.
func (s RoundStatus) Valid() bool {
	return s == RoundBetting || s == RoundSettled
}

// Round is one deal inside a room: a fixed betting window, then a single roll
// of two dice that resolves every bet placed during the window.
type Round struct {
	ID     string
	RoomID string
	// Number is 1-based and unique within the room (1..MaxRounds).
	Number int

	BettingOpensAt  time.Time
	BettingClosesAt time.Time

	// Die1, Die2 and Outcome are zero-valued until the round is settled.
	Die1    int
	Die2    int
	Outcome Outcome

	Status    RoundStatus
	SettledAt *time.Time

	bets map[string]*Bet // playerID -> bet
}

// NewRound opens a round with a betting window of the given duration starting
// at opensAt. The backend clock is authoritative: callers pass a Clock-derived
// time, never a client-supplied one.
func NewRound(id, roomID string, number int, opensAt time.Time, window time.Duration) (*Round, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(roomID) == "" {
		return nil, ErrInvalidID
	}
	if number < 1 || number > MaxRounds {
		return nil, ErrInvalidRoundNumber
	}
	if window <= 0 {
		return nil, ErrInvalidBettingWindow
	}
	opens := opensAt.UTC()
	return &Round{
		ID:              id,
		RoomID:          roomID,
		Number:          number,
		BettingOpensAt:  opens,
		BettingClosesAt: opens.Add(window),
		Status:          RoundBetting,
		bets:            make(map[string]*Bet),
	}, nil
}

// RehydrateRound rebuilds a round from persisted state together with its bets.
func RehydrateRound(id, roomID string, number int, opensAt, closesAt time.Time, die1, die2 int, outcome Outcome, status RoundStatus, settledAt *time.Time, bets []*Bet) (*Round, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(roomID) == "" {
		return nil, ErrInvalidID
	}
	if number < 1 || number > MaxRounds {
		return nil, ErrInvalidRoundNumber
	}
	if !status.Valid() {
		return nil, ErrInvalidRoundNumber
	}
	if !closesAt.After(opensAt) {
		return nil, ErrInvalidBettingWindow
	}
	r := &Round{
		ID:              id,
		RoomID:          roomID,
		Number:          number,
		BettingOpensAt:  opensAt.UTC(),
		BettingClosesAt: closesAt.UTC(),
		Die1:            die1,
		Die2:            die2,
		Outcome:         outcome,
		Status:          status,
		SettledAt:       settledAt,
		bets:            make(map[string]*Bet, len(bets)),
	}
	for _, b := range bets {
		r.bets[b.PlayerID] = b
	}
	return r, nil
}

// BettingOpen reports whether now falls inside the betting window. The window
// is half-open: [BettingOpensAt, BettingClosesAt). A bet that arrives exactly
// at the closing instant is rejected — the backend clock decides.
func (r *Round) BettingOpen(now time.Time) bool {
	if r.Status != RoundBetting {
		return false
	}
	t := now.UTC()
	return !t.Before(r.BettingOpensAt) && t.Before(r.BettingClosesAt)
}

// PlaceBet validates the betting window and the one-bet-per-player rule, then
// records the bet on the round.
func (r *Round) PlaceBet(bet *Bet, now time.Time) error {
	if bet == nil {
		return ErrInvalidID
	}
	if bet.RoundID != r.ID {
		return ErrInvalidID
	}
	if r.Status == RoundSettled {
		return ErrRoundAlreadySettled
	}
	t := now.UTC()
	if t.Before(r.BettingOpensAt) {
		return ErrBettingNotOpen
	}
	if !t.Before(r.BettingClosesAt) {
		return ErrBettingClosed
	}
	if _, exists := r.bets[bet.PlayerID]; exists {
		return ErrDuplicateBet
	}
	r.bets[bet.PlayerID] = bet
	return nil
}

// HasBet reports whether the player already bet in this round.
func (r *Round) HasBet(playerID string) bool {
	_, ok := r.bets[playerID]
	return ok
}

// Bets returns the bets placed in this round, ordered by placement time.
func (r *Round) Bets() []*Bet {
	out := make([]*Bet, 0, len(r.bets))
	for _, b := range r.bets {
		out = append(out, b)
	}
	// Small n (<= MaxPlayers); insertion sort keeps the ordering stable and
	// avoids pulling in sort just for six elements.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].PlacedAt.Before(out[j-1].PlacedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Settle rolls the dice through the supplied roller, resolves every bet and
// moves the round to the settled state. It returns the per-bet results so the
// caller can credit winners and broadcast the outcome.
//
// Settling is only legal once the betting window has closed; the room engine
// calls this from its single per-room goroutine, so no locking is needed here.
func (r *Round) Settle(roller DiceRoller, now time.Time) ([]Result, error) {
	if r.Status == RoundSettled {
		return nil, ErrRoundAlreadySettled
	}
	if roller == nil {
		return nil, ErrNoActiveRound
	}
	die1, die2, err := roller.Roll()
	if err != nil {
		return nil, err
	}
	outcome, err := OutcomeOf(die1, die2)
	if err != nil {
		return nil, err
	}
	return r.settleWith(die1, die2, outcome, now)
}

// SettleWithDice settles a round using pre-determined dice. It exists for
// replay/tests; production code uses Settle with a crypto/rand roller.
func (r *Round) SettleWithDice(die1, die2 int, now time.Time) ([]Result, error) {
	if r.Status == RoundSettled {
		return nil, ErrRoundAlreadySettled
	}
	outcome, err := OutcomeOf(die1, die2)
	if err != nil {
		return nil, err
	}
	return r.settleWith(die1, die2, outcome, now)
}

func (r *Round) settleWith(die1, die2 int, outcome Outcome, now time.Time) ([]Result, error) {
	bets := r.Bets()
	results := make([]Result, 0, len(bets))
	for _, b := range bets {
		if err := b.Settle(outcome); err != nil {
			return nil, err
		}
		res, _ := b.Result()
		results = append(results, res)
	}
	settledAt := now.UTC()
	r.Die1 = die1
	r.Die2 = die2
	r.Outcome = outcome
	r.Status = RoundSettled
	r.SettledAt = &settledAt
	return results, nil
}
