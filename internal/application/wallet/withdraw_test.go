package wallet_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	walletapp "github.com/mateusxis/cassino/internal/application/wallet"
	"github.com/mateusxis/cassino/internal/domain/player"
	"github.com/mateusxis/cassino/internal/infrastructure/clock"
)

func newWithdrawUseCase(players *fakePlayerRepo, transactions *fakeTransactionRepo, rooms *fakeRoomRepo, sessions *fakeSessionStore) *walletapp.WithdrawUseCase {
	return walletapp.NewWithdrawUseCase(fakeTxManager{}, players, transactions, rooms, sessions, &fakeIDGen{}, clock.NewSystem())
}

func TestWithdrawSucceeds(t *testing.T) {
	t.Parallel()

	players := newFakePlayerRepo(mustPlayer(t, "player-1", 1_000))
	transactions := &fakeTransactionRepo{}
	uc := newWithdrawUseCase(players, transactions, &fakeRoomRepo{}, &fakeSessionStore{})

	out, err := uc.Execute(context.Background(), walletapp.WithdrawInput{PlayerID: "player-1", Amount: 400})
	require.NoError(t, err)
	assert.Equal(t, int64(600), out.Balance)

	stored, err := players.GetByID(context.Background(), "player-1")
	require.NoError(t, err)
	assert.Equal(t, int64(600), stored.Balance)

	require.Len(t, transactions.inserted, 1)
	assert.Equal(t, player.TransactionWithdraw, transactions.inserted[0].Type)
	assert.Equal(t, int64(400), transactions.inserted[0].Amount)
}

func TestWithdrawRejectsNonPositiveAmount(t *testing.T) {
	t.Parallel()

	players := newFakePlayerRepo(mustPlayer(t, "player-1", 1_000))
	transactions := &fakeTransactionRepo{}
	uc := newWithdrawUseCase(players, transactions, &fakeRoomRepo{}, &fakeSessionStore{})

	for _, amount := range []int64{0, -1} {
		_, err := uc.Execute(context.Background(), walletapp.WithdrawInput{PlayerID: "player-1", Amount: amount})
		assert.ErrorIs(t, err, player.ErrInvalidAmount)
	}
	assert.Empty(t, transactions.inserted)
}

func TestWithdrawRejectsInsufficientBalance(t *testing.T) {
	t.Parallel()

	players := newFakePlayerRepo(mustPlayer(t, "player-1", 100))
	transactions := &fakeTransactionRepo{}
	uc := newWithdrawUseCase(players, transactions, &fakeRoomRepo{}, &fakeSessionStore{})

	_, err := uc.Execute(context.Background(), walletapp.WithdrawInput{PlayerID: "player-1", Amount: 101})
	assert.ErrorIs(t, err, player.ErrInsufficientBalance)

	stored, err := players.GetByID(context.Background(), "player-1")
	require.NoError(t, err)
	assert.Equal(t, int64(100), stored.Balance, "a rejected withdrawal must not change the balance")
	assert.Empty(t, transactions.inserted)
}

func TestWithdrawBlockedWhileInActiveRoomViaSessionStore(t *testing.T) {
	t.Parallel()

	players := newFakePlayerRepo(mustPlayer(t, "player-1", 1_000))
	transactions := &fakeTransactionRepo{}
	uc := newWithdrawUseCase(players, transactions, &fakeRoomRepo{}, &fakeSessionStore{activeRoom: "room-1"})

	_, err := uc.Execute(context.Background(), walletapp.WithdrawInput{PlayerID: "player-1", Amount: 100})
	assert.ErrorIs(t, err, player.ErrWithdrawalBlocked)
	assert.Zero(t, players.getForUpdateCalls, "the fast Redis check must short-circuit before the row lock is taken")
	assert.Empty(t, transactions.inserted)
}

func TestWithdrawBlockedWhileInActiveRoomViaRoomRepository(t *testing.T) {
	t.Parallel()

	// The Redis session store missed the binding (e.g. it expired or was
	// never set), but PostgreSQL — the source of truth — still shows the
	// player seated in a room. The authoritative in-transaction check must
	// still block the withdrawal.
	players := newFakePlayerRepo(mustPlayer(t, "player-1", 1_000))
	transactions := &fakeTransactionRepo{}
	uc := newWithdrawUseCase(players, transactions, &fakeRoomRepo{activeRoom: "room-1"}, &fakeSessionStore{})

	_, err := uc.Execute(context.Background(), walletapp.WithdrawInput{PlayerID: "player-1", Amount: 100})
	assert.ErrorIs(t, err, player.ErrWithdrawalBlocked)
	assert.Empty(t, transactions.inserted)

	stored, err := players.GetByID(context.Background(), "player-1")
	require.NoError(t, err)
	assert.Equal(t, int64(1_000), stored.Balance)
}
