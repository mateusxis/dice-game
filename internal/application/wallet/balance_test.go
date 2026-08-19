package wallet_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	walletapp "github.com/mateusxis/cassino/internal/application/wallet"
	"github.com/mateusxis/cassino/internal/domain/player"
)

func TestGetBalanceSucceeds(t *testing.T) {
	t.Parallel()

	players := newFakePlayerRepo(mustPlayer(t, "player-1", 4_200))
	uc := walletapp.NewGetBalanceUseCase(players)

	out, err := uc.Execute(context.Background(), "player-1")
	require.NoError(t, err)
	assert.Equal(t, int64(4_200), out.Balance)
}

func TestGetBalanceUnknownPlayer(t *testing.T) {
	t.Parallel()

	players := newFakePlayerRepo()
	uc := walletapp.NewGetBalanceUseCase(players)

	_, err := uc.Execute(context.Background(), "ghost")
	assert.ErrorIs(t, err, player.ErrNotFound)
}
