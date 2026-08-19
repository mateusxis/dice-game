package game_test

import (
	"context"
	"testing"
	"time"

	gameapp "github.com/mateusxis/cassino/internal/application/game"
	"github.com/mateusxis/cassino/internal/infrastructure/clock"
)

// testWindow is the betting window used by the use case and engine tests. It
// is deliberately not 15s: nothing in these tests waits for real time, so a
// short window keeps the fake clock arithmetic readable.
const testWindow = 15 * time.Second

// harness wires every game use case against the in-memory store, a fake clock
// and a fixed dice roller. No test in this package performs I/O.
type harness struct {
	store    *store
	clock    *clock.Fake
	sessions *fakeSessionStore
	cache    *fakeRoomStateStore
	rooms    *fakeRoomRepo
	rounds   *fakeRoundRepo
	bets     *fakeBetRepo
	players  *fakePlayerRepo
	ledger   *fakeTransactionRepo
	ids      *fakeIDGen
	roller   *fixedRoller

	create     *gameapp.CreateRoomUseCase
	list       *gameapp.ListOpenRoomsUseCase
	join       *gameapp.JoinRoomUseCase
	closeRoom  *gameapp.CloseRoomUseCase
	startRound *gameapp.StartRoundUseCase
	placeBet   *gameapp.PlaceBetUseCase
	settle     *gameapp.SettleRoundUseCase
	abort      *gameapp.AbortRoomUseCase
	roomState  *gameapp.RoomStateUseCase
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	s := newStore()
	h := &harness{
		store:    s,
		clock:    clock.NewFake(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)),
		sessions: newFakeSessionStore(),
		cache:    newFakeRoomStateStore(),
		rooms:    &fakeRoomRepo{s: s},
		rounds:   &fakeRoundRepo{s: s},
		bets:     &fakeBetRepo{s: s},
		players:  &fakePlayerRepo{s: s},
		ledger:   &fakeTransactionRepo{s: s},
		ids:      &fakeIDGen{},
		// 2 + 2 = 4, an even sum, unless a test says otherwise.
		roller: &fixedRoller{die1: 2, die2: 2},
	}
	tx := &fakeTxManager{s: s}

	h.create = gameapp.NewCreateRoomUseCase(tx, h.rooms, h.cache, h.sessions, h.ids, h.clock)
	h.list = gameapp.NewListOpenRoomsUseCase(h.rooms, h.cache)
	h.join = gameapp.NewJoinRoomUseCase(tx, h.rooms, h.cache, h.sessions, h.clock)
	h.closeRoom = gameapp.NewCloseRoomUseCase(tx, h.rooms, h.rounds, h.players, h.cache, h.sessions, h.clock)
	h.startRound = gameapp.NewStartRoundUseCase(tx, h.rooms, h.rounds, h.cache, h.ids, h.clock, testWindow)
	h.placeBet = gameapp.NewPlaceBetUseCase(tx, h.rooms, h.rounds, h.bets, h.players, h.ledger, h.ids, h.clock)
	h.settle = gameapp.NewSettleRoundUseCase(tx, h.rooms, h.rounds, h.bets, h.players, h.ledger, h.cache, h.sessions, h.roller, h.ids, h.clock)
	h.abort = gameapp.NewAbortRoomUseCase(tx, h.rooms, h.rounds, h.bets, h.players, h.ledger, h.cache, h.sessions, h.ids, h.clock)
	h.roomState = gameapp.NewRoomStateUseCase(h.rooms, h.rounds)

	return h
}

// seatPlayer registers a player with a balance and binds them to a room, the
// same way CreateRoom/JoinRoom would.
func (h *harness) seatPlayer(id string, balance int64, roomID string) {
	h.store.addPlayer(id, balance)
	if roomID != "" {
		_, _ = h.sessions.BindPlayerToRoom(context.Background(), id, roomID, time.Hour)
	}
}
