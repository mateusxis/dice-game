// Package dice provides the production DiceRoller: two independent faces
// drawn from the operating system's cryptographically secure entropy source.
package dice

import (
	"crypto/rand"
	"fmt"
	"math/big"

	"github.com/mateusxis/cassino/internal/domain/game"
)

// faces is the number of sides on each die.
const faces = 6

// CryptoRoller draws die faces from crypto/rand. It is safe for concurrent use.
//
// crypto/rand.Int performs rejection sampling internally, so each face is
// exactly uniform over 1..6 — there is no modulo bias, which matters because
// any skew in the parity of the sum would move the realised RTP away from the
// designed 0.96.
type CryptoRoller struct{}

var _ game.DiceRoller = CryptoRoller{}

// NewCryptoRoller returns the production roller.
func NewCryptoRoller() CryptoRoller { return CryptoRoller{} }

// Roll returns two independent faces in 1..6. An error means the entropy
// source failed; the caller must abort settlement rather than invent a result.
func (CryptoRoller) Roll() (int, int, error) {
	die1, err := rollOne()
	if err != nil {
		return 0, 0, err
	}
	die2, err := rollOne()
	if err != nil {
		return 0, 0, err
	}
	return die1, die2, nil
}

func rollOne() (int, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(faces))
	if err != nil {
		return 0, fmt.Errorf("dice: crypto/rand failed: %w", err)
	}
	return int(n.Int64()) + 1, nil
}
