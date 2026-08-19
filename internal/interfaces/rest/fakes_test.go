package rest_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/audit"
	"github.com/mateusxis/cassino/internal/domain/game"
	"github.com/mateusxis/cassino/internal/domain/player"
)

// fakeTxManager runs fn directly against the incoming context.
type fakeTxManager struct{}

var _ ports.TxManager = fakeTxManager{}

func (fakeTxManager) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// fakePlayerRepo is a minimal in-memory ports.PlayerRepository guarded by a
// mutex since handler tests may exercise it via net/http/httptest concurrently
// with the test's own assertions.
type fakePlayerRepo struct {
	mu      sync.Mutex
	byID    map[string]*player.Player
	byEmail map[string]*player.Player
}

var _ ports.PlayerRepository = (*fakePlayerRepo)(nil)

func newFakePlayerRepo() *fakePlayerRepo {
	return &fakePlayerRepo{byID: map[string]*player.Player{}, byEmail: map[string]*player.Player{}}
}

func (f *fakePlayerRepo) seed(p *player.Player) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *p
	f.byID[p.ID] = &cp
	f.byEmail[p.Email] = &cp
}

func (f *fakePlayerRepo) Create(_ context.Context, p *player.Player) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.byEmail[p.Email]; ok {
		return player.ErrEmailAlreadyUsed
	}
	cp := *p
	f.byID[p.ID] = &cp
	f.byEmail[p.Email] = &cp
	return nil
}

func (f *fakePlayerRepo) GetByID(_ context.Context, id string) (*player.Player, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.byID[id]
	if !ok {
		return nil, player.ErrNotFound
	}
	cp := *p
	return &cp, nil
}

func (f *fakePlayerRepo) GetByEmail(_ context.Context, email string) (*player.Player, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.byEmail[email]
	if !ok {
		return nil, player.ErrNotFound
	}
	cp := *p
	return &cp, nil
}

func (f *fakePlayerRepo) GetForUpdate(ctx context.Context, id string) (*player.Player, error) {
	return f.GetByID(ctx, id)
}

func (f *fakePlayerRepo) UpdateBalance(_ context.Context, id string, balance int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.byID[id]
	if !ok {
		return player.ErrNotFound
	}
	p.Balance = balance
	f.byEmail[p.Email].Balance = balance
	return nil
}

// fakeTransactionRepo records ledger entries.
type fakeTransactionRepo struct {
	mu       sync.Mutex
	inserted []*player.Transaction
}

var _ ports.TransactionRepository = (*fakeTransactionRepo)(nil)

func (f *fakeTransactionRepo) Insert(_ context.Context, t *player.Transaction) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inserted = append(f.inserted, t)
	return nil
}

func (f *fakeTransactionRepo) ListByPlayer(context.Context, string, int32, int32) ([]*player.Transaction, error) {
	return nil, nil
}

// fakeRoomRepo implements ports.RoomRepository; only FindActiveRoomForPlayer
// is exercised through the wallet use cases under test here.
type fakeRoomRepo struct {
	activeRoom string
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
func (f *fakeRoomRepo) SetCurrentRound(context.Context, string, int) error         { return nil }
func (f *fakeRoomRepo) AddPlayer(context.Context, string, string, time.Time) error { return nil }
func (f *fakeRoomRepo) RemovePlayer(context.Context, string, string) error         { return nil }
func (f *fakeRoomRepo) CountPlayers(context.Context, string) (int, error)          { return 0, nil }
func (f *fakeRoomRepo) FindActiveRoomForPlayer(context.Context, string) (string, error) {
	return f.activeRoom, nil
}

// fakeSessionStore implements ports.PlayerSessionStore.
type fakeSessionStore struct {
	activeRoom string
}

var _ ports.PlayerSessionStore = (*fakeSessionStore)(nil)

func (f *fakeSessionStore) BindPlayerToRoom(context.Context, string, string, time.Duration) (bool, error) {
	return true, nil
}
func (f *fakeSessionStore) ActiveRoom(context.Context, string) (string, error) {
	return f.activeRoom, nil
}
func (f *fakeSessionStore) ReleasePlayer(context.Context, string) error { return nil }

// fakeHasher is a deterministic, non-cryptographic ports.PasswordHasher.
type fakeHasher struct{}

var _ ports.PasswordHasher = fakeHasher{}

func (fakeHasher) Hash(plain string) (string, error) { return "hashed:" + plain, nil }

func (fakeHasher) Compare(hash, plain string) error {
	if hash != "hashed:"+plain {
		return player.ErrInvalidCredentials
	}
	return nil
}

// fakeIDGen hands out predictable, incrementing ids.
type fakeIDGen struct {
	mu sync.Mutex
	n  int
}

var _ ports.IDGenerator = (*fakeIDGen)(nil)

func (f *fakeIDGen) NewID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	return fmt.Sprintf("id-%d", f.n)
}

// fakeClock always returns a fixed instant.
type fakeClock struct{ now time.Time }

var _ ports.Clock = fakeClock{}

func (f fakeClock) Now() time.Time { return f.now }

// fakeTokenService is a minimal, in-memory ports.TokenService: Issue mints an
// opaque token string that Verify looks back up, so no real JWT signing is
// needed to exercise the Authenticator middleware.
type fakeTokenService struct {
	mu     sync.Mutex
	tokens map[string]ports.Claims
}

var _ ports.TokenService = (*fakeTokenService)(nil)

func newFakeTokenService() *fakeTokenService {
	return &fakeTokenService{tokens: map[string]ports.Claims{}}
}

func (f *fakeTokenService) Issue(playerID, email string) (string, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	expiresAt := time.Now().Add(time.Hour)
	token := fmt.Sprintf("token-%s-%d", playerID, len(f.tokens)+1)
	f.tokens[token] = ports.Claims{PlayerID: playerID, Email: email, ExpiresAt: expiresAt}
	return token, expiresAt, nil
}

func (f *fakeTokenService) Verify(token string) (ports.Claims, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	claims, ok := f.tokens[token]
	if !ok {
		return ports.Claims{}, errors.New("fakeTokenService: unknown token")
	}
	if time.Now().After(claims.ExpiresAt) {
		return ports.Claims{}, errors.New("fakeTokenService: expired token")
	}
	return claims, nil
}

// expire marks token as already past its expiry, without removing it, so
// Verify exercises the expired-token path.
func (f *fakeTokenService) expire(token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.tokens[token]
	c.ExpiresAt = time.Now().Add(-time.Minute)
	f.tokens[token] = c
}

// fakeAuditRepo records every entry appended to it.
type fakeAuditRepo struct {
	mu      sync.Mutex
	entries []*audit.Entry
}

var _ ports.AuditRepository = (*fakeAuditRepo)(nil)

func (f *fakeAuditRepo) Append(_ context.Context, e *audit.Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, e)
	return nil
}

func (f *fakeAuditRepo) all() []*audit.Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*audit.Entry, len(f.entries))
	copy(out, f.entries)
	return out
}
