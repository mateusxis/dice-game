package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authapp "github.com/mateusxis/cassino/internal/application/auth"
	"github.com/mateusxis/cassino/internal/domain/player"
)

func seedPlayer(t *testing.T, repo *fakePlayerRepo, id, email, password string) {
	t.Helper()
	p, err := player.New(id, email, "hashed:"+password, time.Now())
	require.NoError(t, err)
	require.NoError(t, repo.Create(context.Background(), p))
}

func TestLoginSucceeds(t *testing.T) {
	t.Parallel()

	repo := newFakePlayerRepo()
	seedPlayer(t, repo, "player-1", "alice@example.com", "correct-horse")

	expiresAt := time.Now().Add(2 * time.Hour)
	tokens := &fakeTokenService{token: "signed-token", expiresAt: expiresAt}
	uc := authapp.NewLoginUseCase(repo, &fakeHasher{}, tokens)

	out, err := uc.Execute(context.Background(), authapp.LoginInput{Email: "Alice@Example.com", Password: "correct-horse"})
	require.NoError(t, err)
	assert.Equal(t, "player-1", out.PlayerID)
	assert.Equal(t, "signed-token", out.Token)
	assert.Equal(t, expiresAt, out.ExpiresAt)
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	t.Parallel()

	repo := newFakePlayerRepo()
	seedPlayer(t, repo, "player-1", "alice@example.com", "correct-horse")

	uc := authapp.NewLoginUseCase(repo, &fakeHasher{}, &fakeTokenService{})

	_, err := uc.Execute(context.Background(), authapp.LoginInput{Email: "alice@example.com", Password: "wrong-password"})
	assert.ErrorIs(t, err, player.ErrInvalidCredentials)
}

func TestLoginRejectsUnknownEmail(t *testing.T) {
	t.Parallel()

	repo := newFakePlayerRepo()
	uc := authapp.NewLoginUseCase(repo, &fakeHasher{}, &fakeTokenService{})

	_, err := uc.Execute(context.Background(), authapp.LoginInput{Email: "nobody@example.com", Password: "whatever1"})
	assert.ErrorIs(t, err, player.ErrInvalidCredentials, "an unknown e-mail must not be distinguishable from a wrong password")
}

func TestLoginRejectsMalformedEmail(t *testing.T) {
	t.Parallel()

	repo := newFakePlayerRepo()
	uc := authapp.NewLoginUseCase(repo, &fakeHasher{}, &fakeTokenService{})

	_, err := uc.Execute(context.Background(), authapp.LoginInput{Email: "not-an-email", Password: "whatever1"})
	assert.ErrorIs(t, err, player.ErrInvalidCredentials)
}
