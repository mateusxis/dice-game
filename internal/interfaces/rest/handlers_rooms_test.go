package rest_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gameapp "github.com/mateusxis/cassino/internal/application/game"
	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/game"
)

// The /rooms handlers are wired to the narrow RoomCreator/RoomLister/
// RoomCloser interfaces, so these tests stub the use cases directly: what is
// under test here is HTTP shape, authentication and error mapping, not the
// room rules (covered in internal/application/game).

type stubRoomCreator struct {
	mu    sync.Mutex
	out   gameapp.CreateRoomOutput
	err   error
	calls []gameapp.CreateRoomInput
}

func (s *stubRoomCreator) Execute(_ context.Context, in gameapp.CreateRoomInput) (gameapp.CreateRoomOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, in)
	if s.err != nil {
		return gameapp.CreateRoomOutput{}, s.err
	}
	out := s.out
	if out.Room.ID == "" {
		out.Room = ports.RoomSummary{
			ID: "room-1", OwnerID: in.OwnerID, Status: game.RoomOpen,
			MaxRounds: game.MaxRounds, MaxPlayers: game.MaxPlayers, PlayerCount: 1,
			CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		}
	}
	return out, nil
}

func (s *stubRoomCreator) lastCall() gameapp.CreateRoomInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[len(s.calls)-1]
}

type stubRoomLister struct {
	mu    sync.Mutex
	out   gameapp.ListOpenRoomsOutput
	err   error
	calls []gameapp.ListOpenRoomsInput
}

func (s *stubRoomLister) Execute(_ context.Context, in gameapp.ListOpenRoomsInput) (gameapp.ListOpenRoomsOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, in)
	if s.err != nil {
		return gameapp.ListOpenRoomsOutput{}, s.err
	}
	return s.out, nil
}

func (s *stubRoomLister) lastCall() gameapp.ListOpenRoomsInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[len(s.calls)-1]
}

type stubRoomCloser struct {
	mu    sync.Mutex
	out   gameapp.CloseRoomOutput
	err   error
	calls []gameapp.CloseRoomInput
}

func (s *stubRoomCloser) CloseRoom(_ context.Context, in gameapp.CloseRoomInput) (gameapp.CloseRoomOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, in)
	if s.err != nil {
		return gameapp.CloseRoomOutput{}, s.err
	}
	return s.out, nil
}

func (s *stubRoomCloser) lastCall() gameapp.CloseRoomInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[len(s.calls)-1]
}

// --- POST /rooms -------------------------------------------------------

func TestCreateRoomHandlerSuccess(t *testing.T) {
	t.Parallel()
	router, deps := newTestRouter(t)
	playerID, token := registerAndLogin(t, router, "owner@example.com", "correct-horse")

	rec := doRequest(t, router, http.MethodPost, "/rooms", token, nil)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "room-1", body["id"])
	assert.Equal(t, playerID, body["owner_id"])
	assert.Equal(t, "open", body["status"])
	assert.Equal(t, float64(6), body["max_players"])
	assert.Equal(t, float64(10), body["max_rounds"])

	assert.Equal(t, playerID, deps.roomCreator.lastCall().OwnerID,
		"the owner comes from the token, never from the body")
}

func TestCreateRoomHandlerRequiresAuthentication(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)

	rec := doRequest(t, router, http.MethodPost, "/rooms", "", nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "unauthorized", decodeError(t, rec).Error.Code)
}

func TestCreateRoomHandlerConflictWhenAlreadyInARoom(t *testing.T) {
	t.Parallel()
	router, deps := newTestRouter(t)
	_, token := registerAndLogin(t, router, "owner@example.com", "correct-horse")
	deps.roomCreator.err = game.ErrAlreadyInAnotherRoom

	rec := doRequest(t, router, http.MethodPost, "/rooms", token, nil)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "already_in_another_room", decodeError(t, rec).Error.Code)
}

// --- GET /rooms --------------------------------------------------------

func TestListRoomsHandlerReturnsOpenRooms(t *testing.T) {
	t.Parallel()
	router, deps := newTestRouter(t)
	_, token := registerAndLogin(t, router, "owner@example.com", "correct-horse")

	deps.roomLister.out = gameapp.ListOpenRoomsOutput{
		Source: "cache",
		Rooms: []ports.RoomSummary{{
			ID: "room-1", OwnerID: "p1", Status: game.RoomOpen,
			CurrentRound: 2, MaxRounds: 10, PlayerCount: 3, MaxPlayers: 6,
		}},
	}

	rec := doRequest(t, router, http.MethodGet, "/rooms?limit=5&offset=10", token, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Rooms  []map[string]any `json:"rooms"`
		Source string           `json:"source"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "cache", body.Source)
	require.Len(t, body.Rooms, 1)
	assert.Equal(t, "room-1", body.Rooms[0]["id"])
	assert.Equal(t, float64(3), body.Rooms[0]["player_count"])

	call := deps.roomLister.lastCall()
	assert.Equal(t, int32(5), call.Limit)
	assert.Equal(t, int32(10), call.Offset)
}

func TestListRoomsHandlerReturnsAnEmptyArrayNotNull(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)
	_, token := registerAndLogin(t, router, "owner@example.com", "correct-horse")

	rec := doRequest(t, router, http.MethodGet, "/rooms", token, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"rooms":[]`, "an empty listing is [] so clients can iterate it blindly")
}

func TestListRoomsHandlerRequiresAuthentication(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)

	rec := doRequest(t, router, http.MethodGet, "/rooms", "", nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// --- DELETE /rooms/{id} ------------------------------------------------

func TestCloseRoomHandlerClosesImmediately(t *testing.T) {
	t.Parallel()
	router, deps := newTestRouter(t)
	playerID, token := registerAndLogin(t, router, "owner@example.com", "correct-horse")

	deps.roomCloser.out = gameapp.CloseRoomOutput{
		RoomID: "room-1", Status: game.RoomClosed, Closed: true, Reason: gameapp.ReasonOwnerClosed,
	}

	rec := doRequest(t, router, http.MethodDelete, "/rooms/room-1", token, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "closed", body["status"])
	assert.Equal(t, true, body["closed"])

	call := deps.roomCloser.lastCall()
	assert.Equal(t, "room-1", call.RoomID)
	assert.Equal(t, playerID, call.RequesterID)
}

func TestCloseRoomHandlerReportsAPendingClose(t *testing.T) {
	t.Parallel()
	router, deps := newTestRouter(t)
	_, token := registerAndLogin(t, router, "owner@example.com", "correct-horse")

	deps.roomCloser.out = gameapp.CloseRoomOutput{
		RoomID: "room-1", Status: game.RoomClosing, Closed: false, Reason: gameapp.ReasonOwnerClosed,
	}

	rec := doRequest(t, router, http.MethodDelete, "/rooms/room-1", token, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "closing", body["status"])
	assert.Equal(t, false, body["closed"], "the room closes only when its round ends")
}

func TestCloseRoomHandlerErrorMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"not the owner", game.ErrNotOwner, http.StatusForbidden, "not_owner"},
		{"unknown room", game.ErrRoomNotFound, http.StatusNotFound, "room_not_found"},
		{"already closed", game.ErrRoomClosed, http.StatusConflict, "room_closed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			router, deps := newTestRouter(t)
			_, token := registerAndLogin(t, router, "owner-"+tc.code+"@example.com", "correct-horse")
			deps.roomCloser.err = tc.err

			rec := doRequest(t, router, http.MethodDelete, "/rooms/room-1", token, nil)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			assert.Equal(t, tc.code, decodeError(t, rec).Error.Code)
		})
	}
}

func TestRoomRoutesAreAudited(t *testing.T) {
	t.Parallel()
	router, deps := newTestRouter(t)
	_, token := registerAndLogin(t, router, "owner@example.com", "correct-horse")

	require.Equal(t, http.StatusCreated, doRequest(t, router, http.MethodPost, "/rooms", token, nil).Code)
	require.Equal(t, http.StatusOK, doRequest(t, router, http.MethodGet, "/rooms", token, nil).Code)

	actions := map[string]bool{}
	for _, entry := range deps.auditRepo.all() {
		actions[entry.Action] = true
	}
	assert.True(t, actions["room.create"], "POST /rooms is audited as room.create")
	assert.True(t, actions["room.list"], "GET /rooms is audited as room.list")
}
