package auth

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/player"
)

// ErrPasswordTooLong is returned for inputs bcrypt cannot hash. bcrypt only
// considers the first 72 bytes, so accepting longer secrets silently would
// make two different passwords interchangeable.
var ErrPasswordTooLong = errors.New("auth: password must be at most 72 bytes")

// BcryptHasher implements ports.PasswordHasher with bcrypt.
type BcryptHasher struct {
	cost int
}

var _ ports.PasswordHasher = (*BcryptHasher)(nil)

// NewBcryptHasher builds a hasher at the given work factor. Values outside
// bcrypt's supported range fall back to the library default.
func NewBcryptHasher(cost int) *BcryptHasher {
	if cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		cost = bcrypt.DefaultCost
	}
	return &BcryptHasher{cost: cost}
}

// Hash returns the bcrypt digest of a plaintext password.
func (h *BcryptHasher) Hash(plain string) (string, error) {
	if len(plain) > 72 {
		return "", ErrPasswordTooLong
	}
	digest, err := bcrypt.GenerateFromPassword([]byte(plain), h.cost)
	if err != nil {
		return "", fmt.Errorf("auth: hash password: %w", err)
	}
	return string(digest), nil
}

// Compare checks a plaintext password against a stored digest. A mismatch
// returns player.ErrInvalidCredentials so callers cannot accidentally leak
// whether the account exists or the password was wrong.
func (h *BcryptHasher) Compare(hash, plain string) error {
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)); err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return player.ErrInvalidCredentials
		}
		return fmt.Errorf("auth: compare password: %w", err)
	}
	return nil
}
