package game_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gameapp "github.com/mateusxis/cassino/internal/application/game"
	"github.com/mateusxis/cassino/internal/domain/game"
	"github.com/mateusxis/cassino/internal/domain/player"
)

// startedRoom seeds a room with two funded members and opens its first
// betting window.
func startedRoom(t *testing.T) (*harness, gameapp.StartRoundOutput) {
	t.Helper()
	h := newHarness(t)
	h.store.seedRoom("room-1", "owner", "bob")
	h.seatPlayer("owner", 10_000, "room-1")
	h.seatPlayer("bob", 10_000, "room-1")

	out, err := h.startRound.Execute(context.Background(), gameapp.StartRoundInput{RoomID: "room-1", RequesterID: "owner"})
	require.NoError(t, err)
	return h, out
}

func TestStartRoundIsOwnerOnly(t *testing.T) {
	h := newHarness(t)
	h.store.seedRoom("room-1", "owner", "bob")
	h.seatPlayer("owner", 10_000, "room-1")
	h.seatPlayer("bob", 10_000, "room-1")

	_, err := h.startRound.Execute(context.Background(), gameapp.StartRoundInput{RoomID: "room-1", RequesterID: "bob"})
	assert.ErrorIs(t, err, game.ErrNotOwner)
}

func TestStartRoundUsesBackendClockForTheWindow(t *testing.T) {
	h, out := startedRoom(t)

	assert.Equal(t, 1, out.RoundNumber)
	assert.Equal(t, h.clock.Now(), out.OpensAt)
	assert.Equal(t, h.clock.Now().Add(testWindow), out.ClosesAt,
		"closes_at is now + BETTING_WINDOW, computed by the backend")
	assert.ElementsMatch(t, []string{"owner", "bob"}, out.Members)
}

func TestStartRoundRejectsASecondLiveRound(t *testing.T) {
	h, _ := startedRoom(t)

	_, err := h.startRound.Execute(context.Background(), gameapp.StartRoundInput{RoomID: "room-1", RequesterID: "owner"})
	assert.ErrorIs(t, err, game.ErrRoundInProgress)
}

func TestStartRoundRejectsAClosingRoom(t *testing.T) {
	h, _ := startedRoom(t)
	_, err := h.closeRoom.Execute(context.Background(), gameapp.CloseRoomInput{RoomID: "room-1", RequesterID: "owner"})
	require.NoError(t, err)

	// Settle the live round so the room is no longer "round in progress"; the
	// close then takes effect and the room refuses further rounds.
	h.clock.Advance(testWindow)
	_, err = h.settle.Execute(context.Background(), gameapp.SettleRoundInput{RoomID: "room-1"})
	require.NoError(t, err)

	_, err = h.startRound.Execute(context.Background(), gameapp.StartRoundInput{RoomID: "room-1", RequesterID: "owner"})
	assert.ErrorIs(t, err, game.ErrRoomClosed)
}

func TestPlaceBetDebitsStakeAndWritesLedgerEntry(t *testing.T) {
	h, started := startedRoom(t)

	out, err := h.placeBet.Execute(context.Background(), gameapp.PlaceBetInput{
		RoomID: "room-1", PlayerID: "bob", Choice: game.ChoiceEven, Amount: 2_500,
	})
	require.NoError(t, err)

	assert.Equal(t, started.RoundID, out.RoundID)
	assert.Equal(t, game.ChoiceEven, out.Choice)
	assert.Equal(t, int64(2_500), out.Amount)
	assert.NotEmpty(t, out.BetID)

	assert.Equal(t, int64(7_500), h.store.balance("bob"), "the stake is debited immediately")

	entries := h.store.ledger(player.TransactionBet)
	require.Len(t, entries, 1)
	assert.Equal(t, "bob", entries[0].PlayerID)
	assert.Equal(t, int64(2_500), entries[0].Amount)
	require.NotNil(t, entries[0].RoundID)
	assert.Equal(t, started.RoundID, *entries[0].RoundID)
}

func TestPlaceBetRejectsASecondBetInTheSameRound(t *testing.T) {
	h, _ := startedRoom(t)
	in := gameapp.PlaceBetInput{RoomID: "room-1", PlayerID: "bob", Choice: game.ChoiceEven, Amount: 1_000}

	_, err := h.placeBet.Execute(context.Background(), in)
	require.NoError(t, err)

	_, err = h.placeBet.Execute(context.Background(), in)
	assert.ErrorIs(t, err, game.ErrDuplicateBet)
	assert.Equal(t, int64(9_000), h.store.balance("bob"), "the rejected bet must not be debited")
	assert.Len(t, h.store.ledger(player.TransactionBet), 1)
}

func TestPlaceBetRejectsAmountAboveBalance(t *testing.T) {
	h, _ := startedRoom(t)

	_, err := h.placeBet.Execute(context.Background(), gameapp.PlaceBetInput{
		RoomID: "room-1", PlayerID: "bob", Choice: game.ChoiceOdd, Amount: 10_001,
	})
	assert.ErrorIs(t, err, player.ErrInsufficientBalance)
	assert.Equal(t, int64(10_000), h.store.balance("bob"))
	assert.Empty(t, h.store.ledger(player.TransactionBet))
	assert.Empty(t, h.store.bets, "the rolled-back transaction leaves no bet row")
}

func TestPlaceBetRejectedAfterTheWindowCloses(t *testing.T) {
	h, _ := startedRoom(t)

	// The window is half-open: exactly at closes_at betting is already over.
	h.clock.Advance(testWindow)

	_, err := h.placeBet.Execute(context.Background(), gameapp.PlaceBetInput{
		RoomID: "room-1", PlayerID: "bob", Choice: game.ChoiceEven, Amount: 1_000,
	})
	assert.ErrorIs(t, err, game.ErrBettingClosed)
	assert.Equal(t, int64(10_000), h.store.balance("bob"))
}

func TestPlaceBetRejectsNonMember(t *testing.T) {
	h, _ := startedRoom(t)
	h.seatPlayer("stranger", 10_000, "")

	_, err := h.placeBet.Execute(context.Background(), gameapp.PlaceBetInput{
		RoomID: "room-1", PlayerID: "stranger", Choice: game.ChoiceEven, Amount: 1_000,
	})
	assert.ErrorIs(t, err, game.ErrNotAMember)
}

func TestPlaceBetWithoutALiveRound(t *testing.T) {
	h := newHarness(t)
	h.store.seedRoom("room-1", "owner")
	h.seatPlayer("owner", 10_000, "room-1")

	_, err := h.placeBet.Execute(context.Background(), gameapp.PlaceBetInput{
		RoomID: "room-1", PlayerID: "owner", Choice: game.ChoiceEven, Amount: 1_000,
	})
	assert.ErrorIs(t, err, game.ErrNoActiveRound)
}

func TestSettleRoundPaysWinnersExactlyOnePointNineTwoTimesStake(t *testing.T) {
	h, started := startedRoom(t)
	h.roller.set(2, 2) // sum 4 -> even

	_, err := h.placeBet.Execute(context.Background(), gameapp.PlaceBetInput{
		RoomID: "room-1", PlayerID: "bob", Choice: game.ChoiceEven, Amount: 2_500,
	})
	require.NoError(t, err)
	_, err = h.placeBet.Execute(context.Background(), gameapp.PlaceBetInput{
		RoomID: "room-1", PlayerID: "owner", Choice: game.ChoiceOdd, Amount: 1_000,
	})
	require.NoError(t, err)

	h.clock.Advance(testWindow)
	out, err := h.settle.Execute(context.Background(), gameapp.SettleRoundInput{RoomID: "room-1"})
	require.NoError(t, err)

	assert.Equal(t, game.OutcomeEven, out.Outcome)
	assert.Equal(t, 4, out.Sum)
	assert.Equal(t, started.RoundID, out.RoundID)
	require.Len(t, out.Winners(), 1)
	assert.Equal(t, "bob", out.Winners()[0].PlayerID)
	assert.Equal(t, int64(4_800), out.Winners()[0].Payout, "2500 * 192 / 100 = 4800")

	// bob: 10000 - 2500 + 4800; owner: 10000 - 1000 and nothing back.
	assert.Equal(t, int64(12_300), h.store.balance("bob"))
	assert.Equal(t, int64(9_000), h.store.balance("owner"))

	payouts := h.store.ledger(player.TransactionPayout)
	require.Len(t, payouts, 1, "only the winner gets a payout ledger row")
	assert.Equal(t, "bob", payouts[0].PlayerID)
	assert.Equal(t, int64(4_800), payouts[0].Amount)
	require.NotNil(t, payouts[0].RoundID)
	assert.Equal(t, started.RoundID, *payouts[0].RoundID)

	assert.Equal(t, map[string]int64{"bob": 12_300, "owner": 9_000}, out.Balances,
		"every member's balance is reported so it can be pushed at round end")
	assert.False(t, out.RoomClosed)
}

func TestSettleRoundIsNotRepeatable(t *testing.T) {
	h, _ := startedRoom(t)
	h.clock.Advance(testWindow)

	_, err := h.settle.Execute(context.Background(), gameapp.SettleRoundInput{RoomID: "room-1"})
	require.NoError(t, err)

	_, err = h.settle.Execute(context.Background(), gameapp.SettleRoundInput{RoomID: "room-1"})
	assert.ErrorIs(t, err, game.ErrNoActiveRound,
		"the settled round is no longer live, so there is nothing left to settle")
}

func TestSettleRoundClosesTheRoomWhenACloseWasPending(t *testing.T) {
	h, _ := startedRoom(t)
	_, err := h.closeRoom.Execute(context.Background(), gameapp.CloseRoomInput{RoomID: "room-1", RequesterID: "owner"})
	require.NoError(t, err)

	h.clock.Advance(testWindow)
	out, err := h.settle.Execute(context.Background(), gameapp.SettleRoundInput{RoomID: "room-1"})
	require.NoError(t, err)

	assert.True(t, out.RoomClosed)
	assert.Equal(t, gameapp.ReasonOwnerClosed, out.CloseReason)
	assert.Equal(t, game.RoomClosed, h.store.roomStatus("room-1"))
	assert.Empty(t, h.sessions.bound("owner"), "members are unbound once the room really closes")
	assert.Empty(t, h.sessions.bound("bob"))
}

func TestSettleRoundClosesTheRoomAfterTheTenthRound(t *testing.T) {
	h := newHarness(t)
	h.store.seedRoom("room-1", "owner")
	h.seatPlayer("owner", 100_000, "room-1")

	var last gameapp.SettleRoundOutput
	for round := 1; round <= game.MaxRounds; round++ {
		_, err := h.startRound.Execute(context.Background(), gameapp.StartRoundInput{RoomID: "room-1", RequesterID: "owner"})
		require.NoError(t, err, "round %d should start", round)

		h.clock.Advance(testWindow)
		out, err := h.settle.Execute(context.Background(), gameapp.SettleRoundInput{RoomID: "room-1"})
		require.NoError(t, err, "round %d should settle", round)
		last = out

		if round < game.MaxRounds {
			assert.False(t, out.RoomClosed, "round %d must not end the room", round)
		}
	}

	assert.True(t, last.RoomClosed)
	assert.Equal(t, gameapp.ReasonMaxRounds, last.CloseReason)
	assert.Equal(t, game.RoomClosed, h.store.roomStatus("room-1"))

	_, err := h.startRound.Execute(context.Background(), gameapp.StartRoundInput{RoomID: "room-1", RequesterID: "owner"})
	assert.ErrorIs(t, err, game.ErrRoomClosed, "an eleventh round is impossible")
}

func TestAbortRoomRefundsOpenStakes(t *testing.T) {
	h, started := startedRoom(t)

	_, err := h.placeBet.Execute(context.Background(), gameapp.PlaceBetInput{
		RoomID: "room-1", PlayerID: "bob", Choice: game.ChoiceEven, Amount: 3_000,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(7_000), h.store.balance("bob"))

	out, err := h.abort.Execute(context.Background(), gameapp.AbortRoomInput{
		RoomID: "room-1", Reason: gameapp.ReasonShutdown,
	})
	require.NoError(t, err)

	assert.True(t, out.Closed)
	assert.Equal(t, gameapp.ReasonShutdown, out.Reason)
	assert.Equal(t, int64(10_000), h.store.balance("bob"), "the stake comes back untouched")

	refunds := h.store.ledger(player.TransactionPayout)
	require.Len(t, refunds, 1, "the refund is a ledger row so the wallet still reconciles")
	assert.Equal(t, int64(3_000), refunds[0].Amount)
	require.NotNil(t, refunds[0].RoundID)
	assert.Equal(t, started.RoundID, *refunds[0].RoundID)

	assert.Empty(t, h.sessions.bound("bob"))
	assert.Equal(t, game.RoomClosed, h.store.roomStatus("room-1"))
}

func TestRecoverRoomsClosesEverythingLeftBehind(t *testing.T) {
	h, _ := startedRoom(t)
	_, err := h.placeBet.Execute(context.Background(), gameapp.PlaceBetInput{
		RoomID: "room-1", PlayerID: "owner", Choice: game.ChoiceOdd, Amount: 4_000,
	})
	require.NoError(t, err)

	recovery := gameapp.NewRecoverRoomsUseCase(h.rooms, h.abort, nil)
	out, err := recovery.Execute(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 1, out.RoomsFound)
	assert.Equal(t, 1, out.RoomsClosed)
	assert.Zero(t, out.Failures)
	assert.Equal(t, game.RoomClosed, h.store.roomStatus("room-1"))
	assert.Equal(t, int64(10_000), h.store.balance("owner"), "the interrupted stake is refunded")

	// Running it again finds nothing: every room is already closed.
	out, err = recovery.Execute(context.Background())
	require.NoError(t, err)
	assert.Zero(t, out.RoomsFound)
}
