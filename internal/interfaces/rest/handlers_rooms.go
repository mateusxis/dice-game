package rest

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	gameapp "github.com/mateusxis/cassino/internal/application/game"
	"github.com/mateusxis/cassino/internal/application/ports"
)

// The room endpoints depend on narrow interfaces rather than concrete use
// cases so the router can be built against a stub — and so DELETE /rooms/{id}
// can be served by the round engine (which turns an immediate close into a
// graceful one) without the HTTP layer knowing that engines exist.
type (
	// RoomCreator opens a room owned by the caller.
	RoomCreator interface {
		Execute(ctx context.Context, in gameapp.CreateRoomInput) (gameapp.CreateRoomOutput, error)
	}
	// RoomLister lists the rooms currently accepting players.
	RoomLister interface {
		Execute(ctx context.Context, in gameapp.ListOpenRoomsInput) (gameapp.ListOpenRoomsOutput, error)
	}
	// RoomCloser applies an owner's close request.
	RoomCloser interface {
		CloseRoom(ctx context.Context, in gameapp.CloseRoomInput) (gameapp.CloseRoomOutput, error)
	}
)

// roomResponse is the wire shape of a room summary.
type roomResponse struct {
	ID           string    `json:"id"`
	OwnerID      string    `json:"owner_id"`
	Status       string    `json:"status"`
	CurrentRound int       `json:"current_round"`
	MaxRounds    int       `json:"max_rounds"`
	PlayerCount  int       `json:"player_count"`
	MaxPlayers   int       `json:"max_players"`
	CreatedAt    time.Time `json:"created_at"`
}

type roomListResponse struct {
	Rooms []roomResponse `json:"rooms"`
	// Source is "cache" or "database"; it makes the Redis fast path visible
	// without a second endpoint.
	Source string `json:"source"`
}

type closeRoomResponse struct {
	RoomID string `json:"room_id"`
	Status string `json:"status"`
	// Closed is false when the close is pending: the room had a live round and
	// will close as soon as it settles.
	Closed bool   `json:"closed"`
	Reason string `json:"reason"`
}

func toRoomResponse(s ports.RoomSummary) roomResponse {
	return roomResponse{
		ID:           s.ID,
		OwnerID:      s.OwnerID,
		Status:       string(s.Status),
		CurrentRound: s.CurrentRound,
		MaxRounds:    s.MaxRounds,
		PlayerCount:  s.PlayerCount,
		MaxPlayers:   s.MaxPlayers,
		CreatedAt:    s.CreatedAt,
	}
}

// CreateRoomHandler handles POST /rooms. The caller becomes the room's owner
// and its first seated player, which also binds them to it: they cannot join
// another room, and their withdrawals are blocked until it closes.
func CreateRoomHandler(uc RoomCreator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			writeError(w, r, errUnauthenticated)
			return
		}

		out, err := uc.Execute(r.Context(), gameapp.CreateRoomInput{OwnerID: claims.PlayerID})
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, toRoomResponse(out.Room))
	}
}

// ListRoomsHandler handles GET /rooms, answering from the Redis cache when it
// can and from PostgreSQL otherwise.
func ListRoomsHandler(uc RoomLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := queryInt(r, "limit")
		offset := queryInt(r, "offset")

		out, err := uc.Execute(r.Context(), gameapp.ListOpenRoomsInput{Limit: limit, Offset: offset})
		if err != nil {
			writeError(w, r, err)
			return
		}

		rooms := make([]roomResponse, 0, len(out.Rooms))
		for _, room := range out.Rooms {
			rooms = append(rooms, toRoomResponse(room))
		}
		writeJSON(w, http.StatusOK, roomListResponse{Rooms: rooms, Source: out.Source})
	}
}

// CloseRoomHandler handles DELETE /rooms/{id}. Only the owner may close a room
// (403 otherwise) and the close is graceful: with a live round the response
// reports closed=false and status="closing", and the round engine finishes the
// round before the room actually ends.
func CloseRoomHandler(uc RoomCloser) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			writeError(w, r, errUnauthenticated)
			return
		}
		roomID := chi.URLParam(r, "id")

		out, err := uc.CloseRoom(r.Context(), gameapp.CloseRoomInput{RoomID: roomID, RequesterID: claims.PlayerID})
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, closeRoomResponse{
			RoomID: out.RoomID,
			Status: string(out.Status),
			Closed: out.Closed,
			Reason: out.Reason,
		})
	}
}

// queryInt reads an optional non-negative integer query parameter; anything
// unparsable is treated as absent, and the use case applies its defaults.
func queryInt(r *http.Request, key string) int32 {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return 0
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return 0
	}
	return int32(v)
}
