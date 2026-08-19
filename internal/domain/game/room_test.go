package game_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mateusxis/cassino/internal/domain/game"
)

var baseTime = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func newTestRoom(t *testing.T) *game.Room {
	t.Helper()
	room, err := game.NewRoom("room-1", "owner", baseTime)
	require.NoError(t, err)
	return room
}

func TestNewRoomSeatsOwner(t *testing.T) {
	t.Parallel()

	room := newTestRoom(t)
	assert.Equal(t, game.RoomOpen, room.Status)
	assert.Equal(t, 1, room.PlayerCount())
	assert.True(t, room.HasPlayer("owner"))
	assert.True(t, room.IsOwner("owner"))
	assert.Equal(t, game.MaxRounds, room.MaxRounds)
	assert.Equal(t, game.MaxPlayers, room.MaxPlayers)
	assert.Equal(t, 0, room.CurrentRound)

	_, err := game.NewRoom("", "owner", baseTime)
	assert.ErrorIs(t, err, game.ErrInvalidID)
	_, err = game.NewRoom("room-1", "", baseTime)
	assert.ErrorIs(t, err, game.ErrInvalidID)
}

func TestRoomJoinCapIsSixPlayers(t *testing.T) {
	t.Parallel()

	room := newTestRoom(t)
	// The owner already occupies seat 1, so five more fit.
	for i := 2; i <= game.MaxPlayers; i++ {
		err := room.Join(fmt.Sprintf("player-%d", i), baseTime.Add(time.Duration(i)*time.Second))
		require.NoError(t, err, "player %d should fit", i)
	}
	assert.Equal(t, game.MaxPlayers, room.PlayerCount())
	assert.True(t, room.Full())

	err := room.Join("player-7", baseTime.Add(time.Minute))
	assert.ErrorIs(t, err, game.ErrRoomFull)
	assert.Equal(t, game.MaxPlayers, room.PlayerCount())
}

func TestRoomRejectsDuplicateJoin(t *testing.T) {
	t.Parallel()

	room := newTestRoom(t)
	require.NoError(t, room.Join("alice", baseTime))

	err := room.Join("alice", baseTime.Add(time.Second))
	assert.ErrorIs(t, err, game.ErrAlreadyJoined)
	assert.Equal(t, 2, room.PlayerCount())

	// The owner is already seated, so re-joining is a duplicate too.
	assert.ErrorIs(t, room.Join("owner", baseTime), game.ErrAlreadyJoined)
}

func TestRoomJoinRejectsBlankPlayer(t *testing.T) {
	t.Parallel()

	room := newTestRoom(t)
	assert.ErrorIs(t, room.Join("  ", baseTime), game.ErrInvalidID)
}

func TestRoomJoinRejectedWhenNotOpen(t *testing.T) {
	t.Parallel()

	t.Run("closing", func(t *testing.T) {
		t.Parallel()
		room := newTestRoom(t)
		_, err := room.StartRound("round-1", baseTime, game.DefaultBettingWindow)
		require.NoError(t, err)
		require.NoError(t, room.Close("owner", baseTime))
		require.Equal(t, game.RoomClosing, room.Status)

		assert.ErrorIs(t, room.Join("late", baseTime), game.ErrRoomNotOpen)
	})

	t.Run("closed", func(t *testing.T) {
		t.Parallel()
		room := newTestRoom(t)
		require.NoError(t, room.Close("owner", baseTime))
		require.Equal(t, game.RoomClosed, room.Status)

		assert.ErrorIs(t, room.Join("late", baseTime), game.ErrRoomClosed)
	})
}

func TestRoomPlayersAreOrderedByJoinTime(t *testing.T) {
	t.Parallel()

	room := newTestRoom(t)
	require.NoError(t, room.Join("carol", baseTime.Add(3*time.Second)))
	require.NoError(t, room.Join("bob", baseTime.Add(2*time.Second)))
	require.NoError(t, room.Join("alice", baseTime.Add(time.Second)))

	assert.Equal(t, []string{"owner", "alice", "bob", "carol"}, room.Players())
}

func TestRoomLeave(t *testing.T) {
	t.Parallel()

	room := newTestRoom(t)
	require.NoError(t, room.Join("alice", baseTime))

	require.NoError(t, room.Leave("alice"))
	assert.False(t, room.HasPlayer("alice"))
	assert.ErrorIs(t, room.Leave("alice"), game.ErrNotAMember)
}

func TestRoomRoundCapIsTenRounds(t *testing.T) {
	t.Parallel()

	room := newTestRoom(t)
	now := baseTime

	for i := 1; i <= game.MaxRounds; i++ {
		round, err := room.StartRound(fmt.Sprintf("round-%d", i), now, game.DefaultBettingWindow)
		require.NoError(t, err, "round %d should start", i)
		assert.Equal(t, i, round.Number)
		assert.Equal(t, i, room.CurrentRound)
		assert.Equal(t, game.MaxRounds-i, room.RoundsRemaining())

		now = now.Add(game.DefaultBettingWindow)
		_, err = room.SettleRoundWithDice(1, 1, now)
		require.NoError(t, err)
		now = now.Add(time.Second)
	}

	// Round 10 settling auto-closes the room, so the eleventh attempt is
	// rejected because the room is closed, not merely capped.
	assert.Equal(t, game.RoomClosed, room.Status)
	require.NotNil(t, room.ClosedAt)

	_, err := room.StartRound("round-11", now, game.DefaultBettingWindow)
	assert.ErrorIs(t, err, game.ErrRoomClosed)
	assert.Equal(t, 0, room.RoundsRemaining())
}

func TestRoomStartRoundGuards(t *testing.T) {
	t.Parallel()

	t.Run("rejects a second live round", func(t *testing.T) {
		t.Parallel()
		room := newTestRoom(t)
		_, err := room.StartRound("round-1", baseTime, game.DefaultBettingWindow)
		require.NoError(t, err)

		_, err = room.StartRound("round-2", baseTime, game.DefaultBettingWindow)
		assert.ErrorIs(t, err, game.ErrRoundInProgress)
	})

	t.Run("rejects an empty room", func(t *testing.T) {
		t.Parallel()
		room := newTestRoom(t)
		require.NoError(t, room.Leave("owner"))

		_, err := room.StartRound("round-1", baseTime, game.DefaultBettingWindow)
		assert.ErrorIs(t, err, game.ErrNoPlayers)
	})

	t.Run("rejects a closing room", func(t *testing.T) {
		t.Parallel()
		room := newTestRoom(t)
		_, err := room.StartRound("round-1", baseTime, game.DefaultBettingWindow)
		require.NoError(t, err)
		require.NoError(t, room.Close("owner", baseTime))

		_, err = room.StartRound("round-2", baseTime, game.DefaultBettingWindow)
		assert.ErrorIs(t, err, game.ErrRoomNotOpen)
	})

	t.Run("rejects a non-positive betting window", func(t *testing.T) {
		t.Parallel()
		room := newTestRoom(t)
		_, err := room.StartRound("round-1", baseTime, 0)
		assert.ErrorIs(t, err, game.ErrInvalidBettingWindow)
	})
}

// The graceful-close path: open -> closing -> closed, with the live round
// always allowed to finish so placed bets are resolved.
func TestRoomGracefulCloseStateMachine(t *testing.T) {
	t.Parallel()

	room := newTestRoom(t)
	require.NoError(t, room.Join("alice", baseTime))

	_, err := room.StartRound("round-1", baseTime, game.DefaultBettingWindow)
	require.NoError(t, err)

	bet, err := game.NewBet("bet-1", "round-1", "alice", game.ChoiceEven, 500, baseTime)
	require.NoError(t, err)
	require.NoError(t, room.PlaceBet(bet, baseTime.Add(time.Second)))

	// Owner asks to close mid-round: the room only enters "closing".
	require.NoError(t, room.Close("owner", baseTime.Add(2*time.Second)))
	assert.Equal(t, game.RoomClosing, room.Status)
	assert.Nil(t, room.ClosedAt)

	// A repeated close request is a no-op, not an error.
	require.NoError(t, room.Close("owner", baseTime.Add(3*time.Second)))
	assert.Equal(t, game.RoomClosing, room.Status)

	// Settling the live round completes the close and still pays the bet.
	settledAt := baseTime.Add(game.DefaultBettingWindow)
	results, err := room.SettleRoundWithDice(2, 2, settledAt)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.True(t, results[0].Won)
	assert.Equal(t, game.Payout(500), results[0].Payout)

	assert.Equal(t, game.RoomClosed, room.Status)
	require.NotNil(t, room.ClosedAt)
	assert.Equal(t, settledAt, *room.ClosedAt)

	// Terminal: no more play, and closing again is an error.
	assert.ErrorIs(t, room.Close("owner", settledAt), game.ErrRoomClosed)
	_, err = room.StartRound("round-2", settledAt, game.DefaultBettingWindow)
	assert.ErrorIs(t, err, game.ErrRoomClosed)
}

func TestRoomCloseWithoutLiveRoundClosesImmediately(t *testing.T) {
	t.Parallel()

	room := newTestRoom(t)
	require.NoError(t, room.Close("owner", baseTime))
	assert.Equal(t, game.RoomClosed, room.Status)
	require.NotNil(t, room.ClosedAt)
}

func TestRoomCloseAfterSettledRoundClosesImmediately(t *testing.T) {
	t.Parallel()

	room := newTestRoom(t)
	_, err := room.StartRound("round-1", baseTime, game.DefaultBettingWindow)
	require.NoError(t, err)
	_, err = room.SettleRoundWithDice(1, 2, baseTime.Add(game.DefaultBettingWindow))
	require.NoError(t, err)
	require.Equal(t, game.RoomOpen, room.Status)

	require.NoError(t, room.Close("owner", baseTime.Add(time.Minute)))
	assert.Equal(t, game.RoomClosed, room.Status)
}

func TestOnlyOwnerCanCloseRoom(t *testing.T) {
	t.Parallel()

	room := newTestRoom(t)
	require.NoError(t, room.Join("alice", baseTime))

	assert.ErrorIs(t, room.Close("alice", baseTime), game.ErrNotOwner)
	assert.Equal(t, game.RoomOpen, room.Status)
}

func TestRoomForceClose(t *testing.T) {
	t.Parallel()

	room := newTestRoom(t)
	room.ForceClose(baseTime)
	assert.Equal(t, game.RoomClosed, room.Status)
	require.NotNil(t, room.ClosedAt)

	// Idempotent: the recorded close time does not move.
	first := *room.ClosedAt
	room.ForceClose(baseTime.Add(time.Hour))
	assert.Equal(t, first, *room.ClosedAt)
}

func TestRoomPlaceBetGuards(t *testing.T) {
	t.Parallel()

	t.Run("rejects a non-member", func(t *testing.T) {
		t.Parallel()
		room := newTestRoom(t)
		_, err := room.StartRound("round-1", baseTime, game.DefaultBettingWindow)
		require.NoError(t, err)

		bet, err := game.NewBet("bet-1", "round-1", "stranger", game.ChoiceOdd, 100, baseTime)
		require.NoError(t, err)
		assert.ErrorIs(t, room.PlaceBet(bet, baseTime), game.ErrNotAMember)
	})

	t.Run("rejects when no round is live", func(t *testing.T) {
		t.Parallel()
		room := newTestRoom(t)
		bet, err := game.NewBet("bet-1", "round-1", "owner", game.ChoiceOdd, 100, baseTime)
		require.NoError(t, err)
		assert.ErrorIs(t, room.PlaceBet(bet, baseTime), game.ErrNoActiveRound)
	})

	t.Run("rejects in a closed room", func(t *testing.T) {
		t.Parallel()
		room := newTestRoom(t)
		require.NoError(t, room.Close("owner", baseTime))
		bet, err := game.NewBet("bet-1", "round-1", "owner", game.ChoiceOdd, 100, baseTime)
		require.NoError(t, err)
		assert.ErrorIs(t, room.PlaceBet(bet, baseTime), game.ErrRoomClosed)
	})
}

func TestRoomSettleRoundGuards(t *testing.T) {
	t.Parallel()

	room := newTestRoom(t)
	_, err := room.SettleRoundWithDice(1, 1, baseTime)
	assert.ErrorIs(t, err, game.ErrNoActiveRound)

	_, err = room.StartRound("round-1", baseTime, game.DefaultBettingWindow)
	require.NoError(t, err)
	_, err = room.SettleRoundWithDice(1, 1, baseTime.Add(game.DefaultBettingWindow))
	require.NoError(t, err)

	_, err = room.SettleRoundWithDice(1, 1, baseTime.Add(time.Minute))
	assert.ErrorIs(t, err, game.ErrRoundAlreadySettled)
}

func TestRoomAttachRound(t *testing.T) {
	t.Parallel()

	// Repositories rehydrate a room and its round separately; AttachRound is
	// how the application layer puts them back together.
	room := newTestRoom(t)
	round, err := game.NewRound("round-1", room.ID, 1, baseTime, game.DefaultBettingWindow)
	require.NoError(t, err)

	room.AttachRound(round)
	assert.Same(t, round, room.CurrentRoundRef())

	bet, err := game.NewBet("bet-1", "round-1", room.OwnerID, game.ChoiceEven, 100, baseTime)
	require.NoError(t, err)
	require.NoError(t, room.PlaceBet(bet, baseTime), "the attached round accepts bets")

	// A settled round is not "live": attaching it clears the pointer, so the
	// room reports no active round instead of accepting a late bet.
	_, err = round.SettleWithDice(1, 1, baseTime.Add(game.DefaultBettingWindow))
	require.NoError(t, err)
	room.AttachRound(round)
	assert.Nil(t, room.CurrentRoundRef())

	room.AttachRound(nil)
	assert.Nil(t, room.CurrentRoundRef())
}
