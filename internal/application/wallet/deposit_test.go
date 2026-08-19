package wallet_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	walletapp "github.com/mateusxis/cassino/internal/application/wallet"
	"github.com/mateusxis/cassino/internal/domain/player"
	"github.com/mateusxis/cassino/internal/infrastructure/clock"
)

func mustPlayer(t *testing.T, id string, balance int64) *player.Player {
	t.Helper()
	p, err := player.Rehydrate(id, id+"@example.com", "hash", balance, time.Now())
	require.NoError(t, err)
	return p
}

func TestDepositSucceedsAndWritesLedgerRow(t *testing.T) {
	t.Parallel()

	players := newFakePlayerRepo(mustPlayer(t, "player-1", 1_000))
	transactions := &fakeTransactionRepo{}
	uc := walletapp.NewDepositUseCase(fakeTxManager{}, players, transactions, &fakeIDGen{}, clock.NewSystem())

	out, err := uc.Execute(context.Background(), walletapp.DepositInput{PlayerID: "player-1", Amount: 500})
	require.NoError(t, err)
	assert.Equal(t, int64(1_500), out.Balance)

	stored, err := players.GetByID(context.Background(), "player-1")
	require.NoError(t, err)
	assert.Equal(t, int64(1_500), stored.Balance)

	require.Len(t, transactions.inserted, 1)
	entry := transactions.inserted[0]
	assert.Equal(t, player.TransactionDeposit, entry.Type)
	assert.Equal(t, int64(500), entry.Amount)
	assert.Equal(t, "player-1", entry.PlayerID)
	assert.Nil(t, entry.RoundID)
}

func TestDepositRejectsNonPositiveAmount(t *testing.T) {
	t.Parallel()

	players := newFakePlayerRepo(mustPlayer(t, "player-1", 1_000))
	transactions := &fakeTransactionRepo{}
	uc := walletapp.NewDepositUseCase(fakeTxManager{}, players, transactions, &fakeIDGen{}, clock.NewSystem())

	for _, amount := range []int64{0, -1, -500} {
		_, err := uc.Execute(context.Background(), walletapp.DepositInput{PlayerID: "player-1", Amount: amount})
		assert.ErrorIs(t, err, player.ErrInvalidAmount, "amount %d must be rejected", amount)
	}

	assert.Empty(t, transactions.inserted, "a rejected deposit must not touch the ledger")
	stored, err := players.GetByID(context.Background(), "player-1")
	require.NoError(t, err)
	assert.Equal(t, int64(1_000), stored.Balance, "a rejected deposit must not change the balance")
}

func TestDepositAllowedWhileInActiveRoom(t *testing.T) {
	t.Parallel()

	// Per the requirements, deposits are never blocked by room membership;
	// the deposit use case does not even take a room/session port, so this
	// test simply documents that a deposit succeeds regardless.
	players := newFakePlayerRepo(mustPlayer(t, "player-1", 1_000))
	transactions := &fakeTransactionRepo{}
	uc := walletapp.NewDepositUseCase(fakeTxManager{}, players, transactions, &fakeIDGen{}, clock.NewSystem())

	out, err := uc.Execute(context.Background(), walletapp.DepositInput{PlayerID: "player-1", Amount: 200})
	require.NoError(t, err)
	assert.Equal(t, int64(1_200), out.Balance)
}
