package auth

import (
	"context"
	"errors"
	"time"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/player"
)

// LoginInput carries the raw credentials submitted at the transport layer.
type LoginInput struct {
	Email    string
	Password string
}

// LoginOutput is the issued session.
type LoginOutput struct {
	PlayerID  string
	Token     string
	ExpiresAt time.Time
}

// LoginUseCase verifies credentials and issues an access token.
type LoginUseCase struct {
	players ports.PlayerRepository
	hasher  ports.PasswordHasher
	tokens  ports.TokenService
}

// NewLoginUseCase wires a LoginUseCase from its ports.
func NewLoginUseCase(players ports.PlayerRepository, hasher ports.PasswordHasher, tokens ports.TokenService) *LoginUseCase {
	return &LoginUseCase{players: players, hasher: hasher, tokens: tokens}
}

// Execute checks the e-mail/password pair and issues a JWT on success. Any
// failure — unknown e-mail, wrong password, malformed e-mail — collapses to
// player.ErrInvalidCredentials so a caller cannot use the response to
// enumerate registered accounts.
func (uc *LoginUseCase) Execute(ctx context.Context, in LoginInput) (LoginOutput, error) {
	email, err := player.NormalizeEmail(in.Email)
	if err != nil {
		return LoginOutput{}, player.ErrInvalidCredentials
	}

	p, err := uc.players.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, player.ErrNotFound) {
			return LoginOutput{}, player.ErrInvalidCredentials
		}
		return LoginOutput{}, err
	}

	if err := uc.hasher.Compare(p.PasswordHash, in.Password); err != nil {
		return LoginOutput{}, err
	}

	token, expiresAt, err := uc.tokens.Issue(p.ID, p.Email)
	if err != nil {
		return LoginOutput{}, err
	}

	return LoginOutput{PlayerID: p.ID, Token: token, ExpiresAt: expiresAt}, nil
}
