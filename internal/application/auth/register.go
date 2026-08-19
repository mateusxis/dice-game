// Package auth holds the authentication use cases: account registration and
// login. Both depend only on ports — no HTTP, no SQL, no JWT library types
// leak in here.
package auth

import (
	"context"
	"time"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/player"
)

// RegisterInput carries the raw credentials submitted at the transport layer.
type RegisterInput struct {
	Email    string
	Password string
}

// RegisterOutput is what the transport layer needs to build a response. It
// never carries the password or its hash.
type RegisterOutput struct {
	PlayerID  string
	Email     string
	CreatedAt time.Time
}

// RegisterUseCase creates a new account with a zero balance. It hashes the
// password, never storing or returning the plaintext.
type RegisterUseCase struct {
	players ports.PlayerRepository
	hasher  ports.PasswordHasher
	ids     ports.IDGenerator
	clock   ports.Clock
}

// NewRegisterUseCase wires a RegisterUseCase from its ports.
func NewRegisterUseCase(players ports.PlayerRepository, hasher ports.PasswordHasher, ids ports.IDGenerator, clock ports.Clock) *RegisterUseCase {
	return &RegisterUseCase{players: players, hasher: hasher, ids: ids, clock: clock}
}

// Execute validates the e-mail and password, hashes the password, and
// persists a new zero-balance account. It returns player.ErrInvalidEmail,
// ErrPasswordTooShort, or player.ErrEmailAlreadyUsed on failure.
func (uc *RegisterUseCase) Execute(ctx context.Context, in RegisterInput) (RegisterOutput, error) {
	email, err := player.NormalizeEmail(in.Email)
	if err != nil {
		return RegisterOutput{}, err
	}
	if len(in.Password) < MinPasswordLength {
		return RegisterOutput{}, ErrPasswordTooShort
	}

	hash, err := uc.hasher.Hash(in.Password)
	if err != nil {
		return RegisterOutput{}, err
	}

	now := uc.clock.Now()
	p, err := player.New(uc.ids.NewID(), email, hash, now)
	if err != nil {
		return RegisterOutput{}, err
	}

	if err := uc.players.Create(ctx, p); err != nil {
		return RegisterOutput{}, err
	}

	return RegisterOutput{PlayerID: p.ID, Email: p.Email, CreatedAt: p.CreatedAt}, nil
}
