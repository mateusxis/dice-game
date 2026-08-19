package game

import (
	"context"
	"sort"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/game"
)

// Paging defaults for the open-room listing.
const (
	defaultRoomPageLimit = 50
	maxRoomPageLimit     = 200
)

// ListOpenRoomsUseCase answers "which tables can I join right now".
type ListOpenRoomsUseCase struct {
	rooms ports.RoomRepository
	state ports.RoomStateStore
}

// NewListOpenRoomsUseCase wires a ListOpenRoomsUseCase from its ports.
func NewListOpenRoomsUseCase(rooms ports.RoomRepository, state ports.RoomStateStore) *ListOpenRoomsUseCase {
	return &ListOpenRoomsUseCase{rooms: rooms, state: state}
}

// Execute returns the open rooms, newest first.
//
// Redis answers first because listing is the hottest read in the system. Two
// cases fall through to PostgreSQL, which is always the source of truth:
// a cache error (treated as a miss, never as a failure) and an empty cache —
// which is indistinguishable from "there are genuinely no open rooms", so the
// database gets the last word and the cache is warmed with what it finds.
//
// Paging is applied by the database; a cache hit returns the whole cached set
// sorted newest-first and then applies the same window in memory, so both
// paths behave the same from the caller's point of view.
func (uc *ListOpenRoomsUseCase) Execute(ctx context.Context, in ListOpenRoomsInput) (ListOpenRoomsOutput, error) {
	limit, offset := normalizePaging(in.Limit, in.Offset)

	if uc.state != nil {
		cached, err := uc.state.ListOpenRooms(ctx)
		if err == nil && len(cached) > 0 {
			open := make([]ports.RoomSummary, 0, len(cached))
			for _, r := range cached {
				if r.Status == game.RoomOpen {
					open = append(open, r)
				}
			}
			if len(open) > 0 {
				sort.Slice(open, func(i, j int) bool { return open[i].CreatedAt.After(open[j].CreatedAt) })
				return ListOpenRoomsOutput{Rooms: page(open, limit, offset), Source: "cache"}, nil
			}
		}
	}

	rooms, err := uc.rooms.ListOpen(ctx, limit, offset)
	if err != nil {
		return ListOpenRoomsOutput{}, err
	}
	if uc.state != nil {
		for _, r := range rooms {
			_ = uc.state.SaveRoom(ctx, r, roomCacheTTL)
		}
	}
	return ListOpenRoomsOutput{Rooms: rooms, Source: "database"}, nil
}

func normalizePaging(limit, offset int32) (int32, int32) {
	if limit <= 0 {
		limit = defaultRoomPageLimit
	}
	if limit > maxRoomPageLimit {
		limit = maxRoomPageLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func page(rooms []ports.RoomSummary, limit, offset int32) []ports.RoomSummary {
	if int(offset) >= len(rooms) {
		return []ports.RoomSummary{}
	}
	end := int(offset) + int(limit)
	if end > len(rooms) {
		end = len(rooms)
	}
	return rooms[offset:end]
}
