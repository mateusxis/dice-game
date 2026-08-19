package wallet_test

import (
	"context"
	"fmt"
	"time"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/game"
	"github.com/mateusxis/cassino/internal/domain/player"
)

// fakeTxManager runs fn directly against the incoming context: it does not
// model real transaction/rollback semantics, which the use case tests do not
// need — they only care that the sequence of port calls inside WithinTx is
// correct.
type fakeTxManager struct{}

var _ ports.TxManager = fakeTxManager{}

func (fakeTxManager) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// fakePlayerRepo is a minimal in-memory ports.PlayerRepository.
type fakePlayerRepo struct {
	byID              map[string]*player.Player
	updateBalanceErr  error
	getForUpdateErr   error
	getForUpdateCalls int
}

var _ ports.PlayerRepository = (*fakePlayerRepo)(nil)

func newFakePlayerRepo(players ...*player.Player) *fakePlayerRepo {
	f := &fakePlayerRepo{byID: map[string]*player.Player{}}
	for _, p := range players {
		cp := *p
		f.byID[p.ID] = &cp
	}
	return f
}

func (f *fakePlayerRepo) Create(context.Context, *player.Player) error { return nil }

func (f *fakePlayerRepo) GetByID(_ context.Context, id string) (*player.Player, error) {
	p, ok := f.byID[id]
	if !ok {
		return nil, player.ErrNotFound
	}
	cp := *p
	return &cp, nil
}

func (f *fakePlayerRepo) GetByEmail(context.Context, string) (*player.Player, error) {
	return nil, player.ErrNotFound
}

func (f *fakePlayerRepo) GetForUpdate(ctx context.Context, id string) (*player.Player, error) {
	f.getForUpdateCalls++
	if f.getForUpdateErr != nil {
		return nil, f.getForUpdateErr
	}
	return f.GetByID(ctx, id)
}

func (f *fakePlayerRepo) UpdateBalance(_ context.Context, id string, balance int64) error {
	if f.updateBalanceErr != nil {
		return f.updateBalanceErr
	}
	p, ok := f.byID[id]
	if !ok {
		return player.ErrNotFound
	}
	p.Balance = balance
	return nil
}

// fakeTransactionRepo records every ledger entry appended.
type fakeTransactionRepo struct {
	inserted  []*player.Transaction
	insertErr error
}

var _ ports.TransactionRepository = (*fakeTransactionRepo)(nil)

func (f *fakeTransactionRepo) Insert(_ context.Context, t *player.Transaction) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	f.inserted = append(f.inserted, t)
	return nil
}

func (f *fakeTransactionRepo) ListByPlayer(context.Context, string, int32, int32) ([]*player.Transaction, error) {
	return f.inserted, nil
}

// fakeIDGen hands out predictable, incrementing ids.
type fakeIDGen struct{ n int }

var _ ports.IDGenerator = (*fakeIDGen)(nil)

func (f *fakeIDGen) NewID() string {
	f.n++
	return fmt.Sprintf("tx-%d", f.n)
}

// fakeRoomRepo implements ports.RoomRepository; only FindActiveRoomForPlayer
// is exercised by the wallet use cases, everything else is unused filler.
type fakeRoomRepo struct {
	activeRoom    string
	activeRoomErr error
}

var _ ports.RoomRepository = (*fakeRoomRepo)(nil)

func (f *fakeRoomRepo) Create(context.Context, *game.Room) error { return nil }
func (f *fakeRoomRepo) GetByID(context.Context, string) (*game.Room, error) {
	return nil, game.ErrRoomNotFound
}
func (f *fakeRoomRepo) GetForUpdate(context.Context, string) (*game.Room, error) {
	return nil, game.ErrRoomNotFound
}
func (f *fakeRoomRepo) ListOpen(context.Context, int32, int32) ([]ports.RoomSummary, error) {
	return nil, nil
}
func (f *fakeRoomRepo) UpdateStatus(context.Context, string, game.RoomStatus, *time.Time) error {
	return nil
}
func (f *fakeRoomRepo) SetCurrentRound(context.Context, string, int) error { return nil }
func (f *fakeRoomRepo) AddPlayer(context.Context, string, string, time.Time) error {
	return nil
}
func (f *fakeRoomRepo) RemovePlayer(context.Context, string, string) error { return nil }
func (f *fakeRoomRepo) CountPlayers(context.Context, string) (int, error)  { return 0, nil }
func (f *fakeRoomRepo) FindActiveRoomForPlayer(context.Context, string) (string, error) {
	if f.activeRoomErr != nil {
		return "", f.activeRoomErr
	}
	return f.activeRoom, nil
}

// fakeSessionStore implements ports.PlayerSessionStore.
type fakeSessionStore struct {
	activeRoom    string
	activeRoomErr error
}

var _ ports.PlayerSessionStore = (*fakeSessionStore)(nil)

func (f *fakeSessionStore) BindPlayerToRoom(context.Context, string, string, time.Duration) (bool, error) {
	return true, nil
}

func (f *fakeSessionStore) ActiveRoom(context.Context, string) (string, error) {
	if f.activeRoomErr != nil {
		return "", f.activeRoomErr
	}
	return f.activeRoom, nil
}

func (f *fakeSessionStore) ReleasePlayer(context.Context, string) error { return nil }
