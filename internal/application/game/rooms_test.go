package game_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gameapp "github.com/mateusxis/cassino/internal/application/game"
	"github.com/mateusxis/cassino/internal/domain/game"
)

func TestCreateRoomSeatsOwnerAndBindsSession(t *testing.T) {
	h := newHarness(t)
	h.seatPlayer("owner", 10_000, "")

	out, err := h.create.Execute(context.Background(), gameapp.CreateRoomInput{OwnerID: "owner"})
	require.NoError(t, err)

	assert.Equal(t, "owner", out.Room.OwnerID)
	assert.Equal(t, game.RoomOpen, out.Room.Status)
	assert.Equal(t, 1, out.Room.PlayerCount, "the creator is seated immediately")
	assert.Equal(t, game.MaxPlayers, out.Room.MaxPlayers)
	assert.Equal(t, game.MaxRounds, out.Room.MaxRounds)

	assert.Equal(t, out.Room.ID, h.sessions.bound("owner"), "creator is bound to the room, blocking withdrawals")

	cached, found, err := h.cache.GetRoom(context.Background(), out.Room.ID)
	require.NoError(t, err)
	require.True(t, found, "the new room is cached for the listing")
	assert.Equal(t, out.Room.ID, cached.ID)
}

func TestCreateRoomRejectsPlayerAlreadyInARoom(t *testing.T) {
	h := newHarness(t)
	h.seatPlayer("owner", 10_000, "")

	_, err := h.create.Execute(context.Background(), gameapp.CreateRoomInput{OwnerID: "owner"})
	require.NoError(t, err)

	_, err = h.create.Execute(context.Background(), gameapp.CreateRoomInput{OwnerID: "owner"})
	assert.ErrorIs(t, err, game.ErrAlreadyInAnotherRoom)
}

func TestCreateRoomReleasesBindingWhenPersistenceFails(t *testing.T) {
	h := newHarness(t)
	h.seatPlayer("owner", 10_000, "")
	boom := errors.New("insert failed")
	h.rooms.createErr = boom

	_, err := h.create.Execute(context.Background(), gameapp.CreateRoomInput{OwnerID: "owner"})
	assert.ErrorIs(t, err, boom)
	assert.Empty(t, h.sessions.bound("owner"), "a failed create must not leave the player locked to a phantom room")
}

func TestListOpenRoomsPrefersCacheAndFallsBackToDatabase(t *testing.T) {
	h := newHarness(t)
	h.seatPlayer("owner", 10_000, "")
	created, err := h.create.Execute(context.Background(), gameapp.CreateRoomInput{OwnerID: "owner"})
	require.NoError(t, err)

	out, err := h.list.Execute(context.Background(), gameapp.ListOpenRoomsInput{})
	require.NoError(t, err)
	assert.Equal(t, "cache", out.Source)
	require.Len(t, out.Rooms, 1)
	assert.Equal(t, created.Room.ID, out.Rooms[0].ID)

	// Cache wiped (restart, eviction): PostgreSQL still answers, and the entry
	// is written back.
	require.NoError(t, h.cache.RemoveRoom(context.Background(), created.Room.ID))

	out, err = h.list.Execute(context.Background(), gameapp.ListOpenRoomsInput{})
	require.NoError(t, err)
	assert.Equal(t, "database", out.Source)
	require.Len(t, out.Rooms, 1)

	_, found, err := h.cache.GetRoom(context.Background(), created.Room.ID)
	require.NoError(t, err)
	assert.True(t, found, "the database fallback warms the cache")
}

func TestListOpenRoomsExcludesClosedRooms(t *testing.T) {
	h := newHarness(t)
	h.seatPlayer("owner", 10_000, "")
	created, err := h.create.Execute(context.Background(), gameapp.CreateRoomInput{OwnerID: "owner"})
	require.NoError(t, err)

	_, err = h.closeRoom.Execute(context.Background(), gameapp.CloseRoomInput{RoomID: created.Room.ID, RequesterID: "owner"})
	require.NoError(t, err)

	out, err := h.list.Execute(context.Background(), gameapp.ListOpenRoomsInput{})
	require.NoError(t, err)
	assert.Empty(t, out.Rooms)
}

func TestJoinRoomSeatsPlayerAndBindsSession(t *testing.T) {
	h := newHarness(t)
	h.store.seedRoom("room-1", "owner")
	h.seatPlayer("owner", 10_000, "room-1")
	h.seatPlayer("bob", 10_000, "")

	out, err := h.join.Execute(context.Background(), gameapp.JoinRoomInput{RoomID: "room-1", PlayerID: "bob"})
	require.NoError(t, err)

	assert.False(t, out.AlreadyMember)
	assert.Equal(t, 2, out.Room.PlayerCount)
	assert.Contains(t, out.Players, "bob")
	assert.Equal(t, "room-1", h.sessions.bound("bob"))
}

func TestJoinRoomIsIdempotentForAnExistingMember(t *testing.T) {
	h := newHarness(t)
	h.store.seedRoom("room-1", "owner")
	h.seatPlayer("owner", 10_000, "room-1")

	// The owner joined over REST; attaching over WebSocket must succeed.
	out, err := h.join.Execute(context.Background(), gameapp.JoinRoomInput{RoomID: "room-1", PlayerID: "owner"})
	require.NoError(t, err)
	assert.True(t, out.AlreadyMember)
	assert.Equal(t, 1, out.Room.PlayerCount)
}

func TestJoinRoomRejectsPlayerInAnotherRoom(t *testing.T) {
	h := newHarness(t)
	h.store.seedRoom("room-1", "owner")
	h.store.seedRoom("room-2", "carol")
	h.seatPlayer("bob", 10_000, "room-2")

	_, err := h.join.Execute(context.Background(), gameapp.JoinRoomInput{RoomID: "room-1", PlayerID: "bob"})
	assert.ErrorIs(t, err, game.ErrAlreadyInAnotherRoom)
}

func TestJoinRoomEnforcesSixSeatCap(t *testing.T) {
	h := newHarness(t)
	h.store.seedRoom("room-1", "p1", "p2", "p3", "p4", "p5", "p6")
	for _, id := range []string{"p1", "p2", "p3", "p4", "p5", "p6"} {
		h.seatPlayer(id, 10_000, "room-1")
	}
	h.seatPlayer("p7", 10_000, "")

	_, err := h.join.Execute(context.Background(), gameapp.JoinRoomInput{RoomID: "room-1", PlayerID: "p7"})
	assert.ErrorIs(t, err, game.ErrRoomFull)
	assert.Empty(t, h.sessions.bound("p7"), "a rejected join must not leave the player bound")
}

func TestJoinRoomRejectsClosedRoom(t *testing.T) {
	h := newHarness(t)
	h.store.seedRoom("room-1", "owner")
	h.seatPlayer("owner", 10_000, "room-1")
	h.seatPlayer("bob", 10_000, "")
	require.NoError(t, h.rooms.UpdateStatus(context.Background(), "room-1", game.RoomClosed, nil))

	_, err := h.join.Execute(context.Background(), gameapp.JoinRoomInput{RoomID: "room-1", PlayerID: "bob"})
	assert.ErrorIs(t, err, game.ErrRoomClosed)
}

func TestJoinRoomFailsWhenSeatMutexStaysHeld(t *testing.T) {
	h := newHarness(t)
	h.store.seedRoom("room-1", "owner")
	h.seatPlayer("owner", 10_000, "room-1")
	h.seatPlayer("bob", 10_000, "")
	h.cache.lockFail = true

	_, err := h.join.Execute(context.Background(), gameapp.JoinRoomInput{RoomID: "room-1", PlayerID: "bob"})
	assert.ErrorIs(t, err, gameapp.ErrJoinInProgress)
}

func TestCloseRoomRequiresOwnership(t *testing.T) {
	h := newHarness(t)
	h.store.seedRoom("room-1", "owner", "bob")
	h.seatPlayer("owner", 10_000, "room-1")
	h.seatPlayer("bob", 10_000, "room-1")

	_, err := h.closeRoom.Execute(context.Background(), gameapp.CloseRoomInput{RoomID: "room-1", RequesterID: "bob"})
	assert.ErrorIs(t, err, game.ErrNotOwner)
	assert.Equal(t, game.RoomOpen, h.store.roomStatus("room-1"))
}

func TestCloseRoomWithoutLiveRoundClosesImmediately(t *testing.T) {
	h := newHarness(t)
	h.store.seedRoom("room-1", "owner", "bob")
	h.seatPlayer("owner", 10_000, "room-1")
	h.seatPlayer("bob", 7_500, "room-1")

	out, err := h.closeRoom.Execute(context.Background(), gameapp.CloseRoomInput{RoomID: "room-1", RequesterID: "owner"})
	require.NoError(t, err)

	assert.True(t, out.Closed)
	assert.Equal(t, game.RoomClosed, out.Status)
	assert.Equal(t, gameapp.ReasonOwnerClosed, out.Reason)
	assert.ElementsMatch(t, []string{"owner", "bob"}, out.Members)
	assert.Equal(t, map[string]int64{"owner": 10_000, "bob": 7_500}, out.Balances,
		"closing pushes a final balance to every member")

	assert.Empty(t, h.sessions.bound("owner"), "closing unbinds members, which unblocks withdrawals")
	assert.Empty(t, h.sessions.bound("bob"))
	_, found, err := h.cache.GetRoom(context.Background(), "room-1")
	require.NoError(t, err)
	assert.False(t, found, "a closed room is evicted from the cache")
}

func TestCloseRoomWithLiveRoundOnlyMarksClosing(t *testing.T) {
	h := newHarness(t)
	h.store.seedRoom("room-1", "owner")
	h.seatPlayer("owner", 10_000, "room-1")

	_, err := h.startRound.Execute(context.Background(), gameapp.StartRoundInput{RoomID: "room-1", RequesterID: "owner"})
	require.NoError(t, err)

	out, err := h.closeRoom.Execute(context.Background(), gameapp.CloseRoomInput{RoomID: "room-1", RequesterID: "owner"})
	require.NoError(t, err)

	assert.False(t, out.Closed, "a live round is always played out")
	assert.Equal(t, game.RoomClosing, out.Status)
	assert.Equal(t, "room-1", h.sessions.bound("owner"), "members stay bound until the room really closes")
}

func TestCloseRoomTwiceFails(t *testing.T) {
	h := newHarness(t)
	h.store.seedRoom("room-1", "owner")
	h.seatPlayer("owner", 10_000, "room-1")

	_, err := h.closeRoom.Execute(context.Background(), gameapp.CloseRoomInput{RoomID: "room-1", RequesterID: "owner"})
	require.NoError(t, err)

	_, err = h.closeRoom.Execute(context.Background(), gameapp.CloseRoomInput{RoomID: "room-1", RequesterID: "owner"})
	assert.ErrorIs(t, err, game.ErrRoomClosed)
}

func TestRoomStateReportsSeatsAndLiveRound(t *testing.T) {
	h := newHarness(t)
	h.store.seedRoom("room-1", "owner", "bob")
	h.seatPlayer("owner", 10_000, "room-1")
	h.seatPlayer("bob", 10_000, "room-1")

	state, err := h.roomState.Execute(context.Background(), "room-1")
	require.NoError(t, err)
	assert.Nil(t, state.Round, "no round has started yet")
	assert.ElementsMatch(t, []string{"owner", "bob"}, state.Players)

	started, err := h.startRound.Execute(context.Background(), gameapp.StartRoundInput{RoomID: "room-1", RequesterID: "owner"})
	require.NoError(t, err)

	state, err = h.roomState.Execute(context.Background(), "room-1")
	require.NoError(t, err)
	require.NotNil(t, state.Round)
	assert.Equal(t, started.RoundID, state.Round.RoundID)
	assert.Equal(t, game.RoundBetting, state.Round.Status)
	assert.Equal(t, started.ClosesAt, state.Round.ClosesAt)
}
