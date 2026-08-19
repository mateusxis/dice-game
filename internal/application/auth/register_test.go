package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authapp "github.com/mateusxis/cassino/internal/application/auth"
	"github.com/mateusxis/cassino/internal/domain/player"
	"github.com/mateusxis/cassino/internal/infrastructure/clock"
)

func TestRegisterSucceeds(t *testing.T) {
	t.Parallel()

	repo := newFakePlayerRepo()
	fixedTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	uc := authapp.NewRegisterUseCase(repo, &fakeHasher{}, &fakeIDGen{}, clock.NewFake(fixedTime))

	out, err := uc.Execute(context.Background(), authapp.RegisterInput{
		Email:    "  Alice@Example.com ",
		Password: "correct-horse",
	})
	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", out.Email)
	assert.NotEmpty(t, out.PlayerID)
	assert.Equal(t, fixedTime, out.CreatedAt)

	stored, err := repo.GetByID(context.Background(), out.PlayerID)
	require.NoError(t, err)
	assert.Zero(t, stored.Balance, "a freshly registered account starts at zero balance")
	assert.Equal(t, "hashed:correct-horse", stored.PasswordHash, "the plaintext password must never be persisted")
}

func TestRegisterRejectsDuplicateEmail(t *testing.T) {
	t.Parallel()

	repo := newFakePlayerRepo()
	uc := authapp.NewRegisterUseCase(repo, &fakeHasher{}, &fakeIDGen{}, clock.NewFake(time.Now()))

	_, err := uc.Execute(context.Background(), authapp.RegisterInput{Email: "alice@example.com", Password: "correct-horse"})
	require.NoError(t, err)

	_, err = uc.Execute(context.Background(), authapp.RegisterInput{Email: "ALICE@example.com", Password: "another-pass"})
	assert.ErrorIs(t, err, player.ErrEmailAlreadyUsed)
}

func TestRegisterRejectsWeakPassword(t *testing.T) {
	t.Parallel()

	repo := newFakePlayerRepo()
	uc := authapp.NewRegisterUseCase(repo, &fakeHasher{}, &fakeIDGen{}, clock.NewFake(time.Now()))

	_, err := uc.Execute(context.Background(), authapp.RegisterInput{Email: "alice@example.com", Password: "short"})
	assert.ErrorIs(t, err, authapp.ErrPasswordTooShort)

	_, ok := repo.byEmail["alice@example.com"]
	assert.False(t, ok, "a rejected registration must not persist a player")
}

func TestRegisterRejectsInvalidEmail(t *testing.T) {
	t.Parallel()

	repo := newFakePlayerRepo()
	uc := authapp.NewRegisterUseCase(repo, &fakeHasher{}, &fakeIDGen{}, clock.NewFake(time.Now()))

	for _, email := range []string{"", "not-an-email", "alice@", "@example.com"} {
		_, err := uc.Execute(context.Background(), authapp.RegisterInput{Email: email, Password: "correct-horse"})
		assert.ErrorIs(t, err, player.ErrInvalidEmail, "email %q must be rejected", email)
	}
}
