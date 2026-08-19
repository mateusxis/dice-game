package player_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mateusxis/cassino/internal/domain/player"
)

var createdAt = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func TestNewPlayerNormalizesEmailAndStartsAtZero(t *testing.T) {
	t.Parallel()

	p, err := player.New("id-1", "  Alice@Example.COM ", "hash", createdAt)
	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", p.Email)
	assert.Zero(t, p.Balance)
	assert.Equal(t, createdAt, p.CreatedAt)
}

func TestNewPlayerValidation(t *testing.T) {
	t.Parallel()

	_, err := player.New("", "alice@example.com", "hash", createdAt)
	assert.ErrorIs(t, err, player.ErrInvalidID)

	_, err = player.New("id-1", "alice@example.com", "   ", createdAt)
	assert.ErrorIs(t, err, player.ErrInvalidPasswordHash)

	for _, email := range []string{"", "alice", "alice@", "@example.com", "a@b@c.com", "alice@localhost", "alice @example.com"} {
		_, err := player.New("id-1", email, "hash", createdAt)
		assert.ErrorIs(t, err, player.ErrInvalidEmail, "email %q must be rejected", email)
	}
}

func TestRehydrateRejectsCorruptRows(t *testing.T) {
	t.Parallel()

	p, err := player.Rehydrate("id-1", "alice@example.com", "hash", 5000, createdAt)
	require.NoError(t, err)
	assert.Equal(t, int64(5000), p.Balance)

	_, err = player.Rehydrate("", "alice@example.com", "hash", 0, createdAt)
	assert.ErrorIs(t, err, player.ErrInvalidID)

	_, err = player.Rehydrate("id-1", "alice@example.com", "hash", -1, createdAt)
	assert.ErrorIs(t, err, player.ErrNegativeBalance)
}

func TestCreditAndDebit(t *testing.T) {
	t.Parallel()

	p, err := player.New("id-1", "alice@example.com", "hash", createdAt)
	require.NoError(t, err)

	require.NoError(t, p.Credit(10_000))
	assert.Equal(t, int64(10_000), p.Balance)

	require.NoError(t, p.Debit(2_500))
	assert.Equal(t, int64(7_500), p.Balance)

	// Spending the exact balance is allowed; one cent more is not.
	require.NoError(t, p.Debit(7_500))
	assert.Zero(t, p.Balance)
	assert.ErrorIs(t, p.Debit(1), player.ErrInsufficientBalance)

	for _, amount := range []int64{0, -1} {
		assert.ErrorIs(t, p.Credit(amount), player.ErrInvalidAmount)
		assert.ErrorIs(t, p.Debit(amount), player.ErrInvalidAmount)
	}
}

func TestDebitLeavesBalanceUntouchedOnFailure(t *testing.T) {
	t.Parallel()

	p, err := player.Rehydrate("id-1", "alice@example.com", "hash", 100, createdAt)
	require.NoError(t, err)

	assert.ErrorIs(t, p.Debit(101), player.ErrInsufficientBalance)
	assert.Equal(t, int64(100), p.Balance)
}

func TestCanAfford(t *testing.T) {
	t.Parallel()

	p, err := player.Rehydrate("id-1", "alice@example.com", "hash", 1000, createdAt)
	require.NoError(t, err)

	assert.True(t, p.CanAfford(1))
	assert.True(t, p.CanAfford(1000))
	assert.False(t, p.CanAfford(1001))
	assert.False(t, p.CanAfford(0), "a non-positive stake is never affordable")
}

func TestNewTransaction(t *testing.T) {
	t.Parallel()

	roundID := "round-1"
	tx, err := player.NewTransaction("tx-1", "id-1", player.TransactionBet, 500, &roundID, createdAt)
	require.NoError(t, err)
	assert.Equal(t, player.TransactionBet, tx.Type)
	require.NotNil(t, tx.RoundID)
	assert.Equal(t, roundID, *tx.RoundID)

	_, err = player.NewTransaction("tx-1", "id-1", player.TransactionType("bonus"), 500, nil, createdAt)
	assert.ErrorIs(t, err, player.ErrInvalidTransactionType)

	_, err = player.NewTransaction("tx-1", "id-1", player.TransactionDeposit, 0, nil, createdAt)
	assert.ErrorIs(t, err, player.ErrInvalidAmount)

	_, err = player.NewTransaction("", "id-1", player.TransactionDeposit, 100, nil, createdAt)
	assert.ErrorIs(t, err, player.ErrInvalidID)
}

func TestTransactionTypeValid(t *testing.T) {
	t.Parallel()

	for _, tt := range []player.TransactionType{
		player.TransactionDeposit,
		player.TransactionWithdraw,
		player.TransactionBet,
		player.TransactionPayout,
	} {
		assert.True(t, tt.Valid(), "%s must be a valid type", tt)
	}
	assert.False(t, player.TransactionType("refund").Valid())
}
