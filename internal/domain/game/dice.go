package game

// DiceRoller produces the two die faces for a round. The domain declares the
// interface; the cryptographically secure implementation lives in
// internal/infrastructure/dice so the domain keeps a stdlib-only import set
// and tests can substitute a deterministic roller.
type DiceRoller interface {
	// Roll returns two independent die faces, each uniformly distributed over
	// 1..6. It returns an error only if the underlying entropy source fails,
	// in which case the round must not be settled.
	Roll() (die1 int, die2 int, err error)
}

// Outcome is the parity of the sum of the two dice.
type Outcome string

const (
	// OutcomeEven means die1+die2 is even.
	OutcomeEven Outcome = "even"
	// OutcomeOdd means die1+die2 is odd.
	OutcomeOdd Outcome = "odd"
)

// Valid reports whether o is a known outcome.
func (o Outcome) Valid() bool {
	return o == OutcomeEven || o == OutcomeOdd
}

// String implements fmt.Stringer.
func (o Outcome) String() string { return string(o) }

// OutcomeOf returns the parity of the dice sum, validating both faces.
func OutcomeOf(die1, die2 int) (Outcome, error) {
	if !validDie(die1) || !validDie(die2) {
		return "", ErrInvalidDie
	}
	if (die1+die2)%2 == 0 {
		return OutcomeEven, nil
	}
	return OutcomeOdd, nil
}

func validDie(d int) bool { return d >= 1 && d <= 6 }
