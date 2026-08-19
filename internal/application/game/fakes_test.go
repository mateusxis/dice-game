package game_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/game"
	"github.com/mateusxis/cassino/internal/domain/player"
)

// ---------------------------------------------------------------------------
// store: one in-memory database shared by every repository fake
// ---------------------------------------------------------------------------

type roomRecord struct {
	id           string
	ownerID      string
	status       game.RoomStatus
	currentRound int
	createdAt    time.Time
	closedAt     *time.Time
	seats        map[string]time.Time
}

type roundRecord struct {
	id        string
	roomID    string
	number    int
	opensAt   time.Time
	closesAt  time.Time
	die1      int
	die2      int
	outcome   game.Outcome
	status    game.RoundStatus
	settledAt *time.Time
}

// store models the persistent side of the system closely enough for the use
// cases to be exercised honestly: seat sets, the (round, player) uniqueness of
// bets, the "settle only while betting" guard, and the wallet ledger.
type store struct {
	mu           sync.Mutex
	players      map[string]*player.Player
	rooms        map[string]*roomRecord
	rounds       map[string]*roundRecord
	bets         map[string]*game.Bet
	transactions []*player.Transaction
}

func newStore() *store {
	return &store{
		players: map[string]*player.Player{},
		rooms:   map[string]*roomRecord{},
		rounds:  map[string]*roundRecord{},
		bets:    map[string]*game.Bet{},
	}
}

func (s *store) addPlayer(id string, balance int64) *player.Player {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := &player.Player{ID: id, Email: id + "@example.test", PasswordHash: "hash", Balance: balance}
	s.players[id] = p
	return p
}

func (s *store) balance(id string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.players[id].Balance
}

// ledger returns every transaction of one type, in insertion order.
func (s *store) ledger(kind player.TransactionType) []*player.Transaction {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*player.Transaction
	for _, t := range s.transactions {
		if t.Type == kind {
			out = append(out, t)
		}
	}
	return out
}

func (s *store) roomStatus(id string) game.RoomStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rooms[id].status
}

// seedRoom inserts an open room with the given members (first one owns it).
func (s *store) seedRoom(id string, members ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seats := map[string]time.Time{}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, m := range members {
		seats[m] = base.Add(time.Duration(i) * time.Second)
	}
	s.rooms[id] = &roomRecord{
		id:        id,
		ownerID:   members[0],
		status:    game.RoomOpen,
		createdAt: base,
		seats:     seats,
	}
}

// snapshot/restore give the fake transaction manager real rollback semantics,
// so a failed use case leaves no half-written state behind — which is exactly
// what the tests about insufficient balance and duplicate bets check.
func (s *store) snapshot() *store {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := newStore()
	for id, p := range s.players {
		clone := *p
		cp.players[id] = &clone
	}
	for id, r := range s.rooms {
		clone := *r
		clone.seats = map[string]time.Time{}
		for k, v := range r.seats {
			clone.seats[k] = v
		}
		cp.rooms[id] = &clone
	}
	for id, r := range s.rounds {
		clone := *r
		cp.rounds[id] = &clone
	}
	for id, b := range s.bets {
		clone := *b
		cp.bets[id] = &clone
	}
	cp.transactions = append(cp.transactions, s.transactions...)
	return cp
}

func (s *store) restore(from *store) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.players = from.players
	s.rooms = from.rooms
	s.rounds = from.rounds
	s.bets = from.bets
	s.transactions = from.transactions
}

// ---------------------------------------------------------------------------
// transaction manager
// ---------------------------------------------------------------------------

type fakeTxManager struct {
	s     *store
	depth int
	mu    sync.Mutex
}

var _ ports.TxManager = (*fakeTxManager)(nil)

func (f *fakeTxManager) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	f.mu.Lock()
	nested := f.depth > 0
	f.depth++
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.depth--
		f.mu.Unlock()
	}()

	if nested {
		return fn(ctx)
	}
	before := f.s.snapshot()
	if err := fn(ctx); err != nil {
		f.s.restore(before)
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// repositories
// ---------------------------------------------------------------------------

type fakePlayerRepo struct{ s *store }

var _ ports.PlayerRepository = (*fakePlayerRepo)(nil)

func (f *fakePlayerRepo) Create(context.Context, *player.Player) error { return nil }

func (f *fakePlayerRepo) GetByID(_ context.Context, id string) (*player.Player, error) {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	p, ok := f.s.players[id]
	if !ok {
		return nil, player.ErrNotFound
	}
	clone := *p
	return &clone, nil
}

func (f *fakePlayerRepo) GetByEmail(context.Context, string) (*player.Player, error) {
	return nil, player.ErrNotFound
}

func (f *fakePlayerRepo) GetForUpdate(ctx context.Context, id string) (*player.Player, error) {
	return f.GetByID(ctx, id)
}

func (f *fakePlayerRepo) UpdateBalance(_ context.Context, id string, balance int64) error {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	p, ok := f.s.players[id]
	if !ok {
		return player.ErrNotFound
	}
	p.Balance = balance
	return nil
}

type fakeTransactionRepo struct{ s *store }

var _ ports.TransactionRepository = (*fakeTransactionRepo)(nil)

func (f *fakeTransactionRepo) Insert(_ context.Context, t *player.Transaction) error {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	clone := *t
	f.s.transactions = append(f.s.transactions, &clone)
	return nil
}

func (f *fakeTransactionRepo) ListByPlayer(context.Context, string, int32, int32) ([]*player.Transaction, error) {
	return nil, nil
}

type fakeRoomRepo struct {
	s *store
	// createErr, addPlayerErr let a test force the database half of a race to
	// fail after the cache half succeeded.
	createErr    error
	addPlayerErr error
}

var (
	_ ports.RoomRepository         = (*fakeRoomRepo)(nil)
	_ ports.RoomRecoveryRepository = (*fakeRoomRepo)(nil)
)

func (f *fakeRoomRepo) Create(_ context.Context, r *game.Room) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	seats := map[string]time.Time{}
	for _, id := range r.Players() {
		seats[id] = r.CreatedAt
	}
	f.s.rooms[r.ID] = &roomRecord{
		id:           r.ID,
		ownerID:      r.OwnerID,
		status:       r.Status,
		currentRound: r.CurrentRound,
		createdAt:    r.CreatedAt,
		closedAt:     r.ClosedAt,
		seats:        seats,
	}
	return nil
}

func (f *fakeRoomRepo) GetByID(_ context.Context, id string) (*game.Room, error) {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	rec, ok := f.s.rooms[id]
	if !ok {
		return nil, game.ErrRoomNotFound
	}
	seats := map[string]time.Time{}
	for k, v := range rec.seats {
		seats[k] = v
	}
	return game.RehydrateRoom(rec.id, rec.ownerID, rec.status, rec.currentRound, rec.createdAt, rec.closedAt, seats, nil)
}

func (f *fakeRoomRepo) GetForUpdate(ctx context.Context, id string) (*game.Room, error) {
	return f.GetByID(ctx, id)
}

func (f *fakeRoomRepo) ListOpen(_ context.Context, limit, offset int32) ([]ports.RoomSummary, error) {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	var out []ports.RoomSummary
	for _, rec := range f.s.rooms {
		if rec.status != game.RoomOpen {
			continue
		}
		out = append(out, ports.RoomSummary{
			ID:           rec.id,
			OwnerID:      rec.ownerID,
			Status:       rec.status,
			CurrentRound: rec.currentRound,
			MaxRounds:    game.MaxRounds,
			PlayerCount:  len(rec.seats),
			MaxPlayers:   game.MaxPlayers,
			CreatedAt:    rec.createdAt,
		})
	}
	if int(offset) >= len(out) {
		return []ports.RoomSummary{}, nil
	}
	end := int(offset) + int(limit)
	if end > len(out) {
		end = len(out)
	}
	return out[offset:end], nil
}

func (f *fakeRoomRepo) ListActiveRoomIDs(context.Context) ([]string, error) {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	var out []string
	for id, rec := range f.s.rooms {
		if rec.status != game.RoomClosed {
			out = append(out, id)
		}
	}
	return out, nil
}

func (f *fakeRoomRepo) UpdateStatus(_ context.Context, id string, status game.RoomStatus, closedAt *time.Time) error {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	rec, ok := f.s.rooms[id]
	if !ok {
		return game.ErrRoomNotFound
	}
	rec.status = status
	rec.closedAt = closedAt
	return nil
}

func (f *fakeRoomRepo) SetCurrentRound(_ context.Context, id string, currentRound int) error {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	rec, ok := f.s.rooms[id]
	if !ok {
		return game.ErrRoomNotFound
	}
	rec.currentRound = currentRound
	return nil
}

func (f *fakeRoomRepo) AddPlayer(_ context.Context, roomID, playerID string, joinedAt time.Time) error {
	if f.addPlayerErr != nil {
		return f.addPlayerErr
	}
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	rec, ok := f.s.rooms[roomID]
	if !ok {
		return game.ErrRoomNotFound
	}
	if _, exists := rec.seats[playerID]; exists {
		return game.ErrAlreadyJoined
	}
	rec.seats[playerID] = joinedAt
	return nil
}

func (f *fakeRoomRepo) RemovePlayer(_ context.Context, roomID, playerID string) error {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	rec, ok := f.s.rooms[roomID]
	if !ok {
		return game.ErrRoomNotFound
	}
	if _, exists := rec.seats[playerID]; !exists {
		return game.ErrNotAMember
	}
	delete(rec.seats, playerID)
	return nil
}

func (f *fakeRoomRepo) CountPlayers(_ context.Context, roomID string) (int, error) {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	rec, ok := f.s.rooms[roomID]
	if !ok {
		return 0, game.ErrRoomNotFound
	}
	return len(rec.seats), nil
}

func (f *fakeRoomRepo) FindActiveRoomForPlayer(_ context.Context, playerID string) (string, error) {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	for id, rec := range f.s.rooms {
		if rec.status == game.RoomClosed {
			continue
		}
		if _, ok := rec.seats[playerID]; ok {
			return id, nil
		}
	}
	return "", nil
}

type fakeRoundRepo struct{ s *store }

var _ ports.RoundRepository = (*fakeRoundRepo)(nil)

func (f *fakeRoundRepo) Create(_ context.Context, r *game.Round) error {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	for _, existing := range f.s.rounds {
		if existing.roomID == r.RoomID && existing.number == r.Number {
			return game.ErrRoundInProgress
		}
	}
	f.s.rounds[r.ID] = &roundRecord{
		id:       r.ID,
		roomID:   r.RoomID,
		number:   r.Number,
		opensAt:  r.BettingOpensAt,
		closesAt: r.BettingClosesAt,
		status:   game.RoundBetting,
	}
	return nil
}

func (f *fakeRoundRepo) GetByID(ctx context.Context, id string) (*game.Round, error) {
	f.s.mu.Lock()
	rec, ok := f.s.rounds[id]
	f.s.mu.Unlock()
	if !ok {
		return nil, game.ErrRoundNotFound
	}
	return f.hydrate(ctx, rec)
}

func (f *fakeRoundRepo) GetByRoomAndNumber(ctx context.Context, roomID string, number int) (*game.Round, error) {
	f.s.mu.Lock()
	var found *roundRecord
	for _, rec := range f.s.rounds {
		if rec.roomID == roomID && rec.number == number {
			found = rec
		}
	}
	f.s.mu.Unlock()
	if found == nil {
		return nil, game.ErrRoundNotFound
	}
	return f.hydrate(ctx, found)
}

func (f *fakeRoundRepo) GetCurrent(ctx context.Context, roomID string) (*game.Round, error) {
	f.s.mu.Lock()
	var found *roundRecord
	for _, rec := range f.s.rounds {
		if rec.roomID != roomID {
			continue
		}
		if found == nil || rec.number > found.number {
			found = rec
		}
	}
	f.s.mu.Unlock()
	if found == nil {
		return nil, game.ErrRoundNotFound
	}
	return f.hydrate(ctx, found)
}

func (f *fakeRoundRepo) ListByRoom(ctx context.Context, roomID string) ([]*game.Round, error) {
	f.s.mu.Lock()
	var recs []*roundRecord
	for _, rec := range f.s.rounds {
		if rec.roomID == roomID {
			recs = append(recs, rec)
		}
	}
	f.s.mu.Unlock()

	out := make([]*game.Round, 0, len(recs))
	for _, rec := range recs {
		round, err := f.hydrate(ctx, rec)
		if err != nil {
			return nil, err
		}
		out = append(out, round)
	}
	return out, nil
}

func (f *fakeRoundRepo) Settle(_ context.Context, r *game.Round) error {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	rec, ok := f.s.rounds[r.ID]
	if !ok {
		return game.ErrRoundNotFound
	}
	if rec.status != game.RoundBetting {
		return game.ErrRoundAlreadySettled
	}
	rec.die1, rec.die2, rec.outcome = r.Die1, r.Die2, r.Outcome
	rec.status = game.RoundSettled
	rec.settledAt = r.SettledAt
	return nil
}

func (f *fakeRoundRepo) hydrate(ctx context.Context, rec *roundRecord) (*game.Round, error) {
	bets, err := (&fakeBetRepo{s: f.s}).ListByRound(ctx, rec.id)
	if err != nil {
		return nil, err
	}
	return game.RehydrateRound(rec.id, rec.roomID, rec.number, rec.opensAt, rec.closesAt,
		rec.die1, rec.die2, rec.outcome, rec.status, rec.settledAt, bets)
}

type fakeBetRepo struct{ s *store }

var _ ports.BetRepository = (*fakeBetRepo)(nil)

func (f *fakeBetRepo) Insert(_ context.Context, b *game.Bet) error {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	for _, existing := range f.s.bets {
		if existing.RoundID == b.RoundID && existing.PlayerID == b.PlayerID {
			return game.ErrDuplicateBet
		}
	}
	clone := *b
	f.s.bets[b.ID] = &clone
	return nil
}

func (f *fakeBetRepo) GetByRoundAndPlayer(_ context.Context, roundID, playerID string) (*game.Bet, error) {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	for _, b := range f.s.bets {
		if b.RoundID == roundID && b.PlayerID == playerID {
			clone := *b
			return &clone, nil
		}
	}
	return nil, game.ErrBetNotFound
}

func (f *fakeBetRepo) ListByRound(_ context.Context, roundID string) ([]*game.Bet, error) {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	var out []*game.Bet
	for _, b := range f.s.bets {
		if b.RoundID != roundID {
			continue
		}
		clone := *b
		out = append(out, &clone)
	}
	// Deterministic order: by placement time, then id.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && lessBet(out[j], out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

func lessBet(a, b *game.Bet) bool {
	if a.PlacedAt.Equal(b.PlacedAt) {
		return a.ID < b.ID
	}
	return a.PlacedAt.Before(b.PlacedAt)
}

func (f *fakeBetRepo) SettleBet(_ context.Context, betID string, won bool, payout int64) error {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	b, ok := f.s.bets[betID]
	if !ok {
		return game.ErrBetNotFound
	}
	if b.Won != nil {
		return game.ErrRoundAlreadySettled
	}
	w, p := won, payout
	b.Won, b.Payout = &w, &p
	return nil
}

// ---------------------------------------------------------------------------
// cache / coordination
// ---------------------------------------------------------------------------

// fakeRoomStateStore models the Redis room cache, including a real (in-memory)
// join mutex so the seat-cap path is exercised rather than stubbed away.
type fakeRoomStateStore struct {
	mu       sync.Mutex
	rooms    map[string]ports.RoomSummary
	locks    map[string]bool
	lockFail bool
	saveErr  error
}

var _ ports.RoomStateStore = (*fakeRoomStateStore)(nil)

func newFakeRoomStateStore() *fakeRoomStateStore {
	return &fakeRoomStateStore{rooms: map[string]ports.RoomSummary{}, locks: map[string]bool{}}
}

func (f *fakeRoomStateStore) SaveRoom(_ context.Context, s ports.RoomSummary, _ time.Duration) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rooms[s.ID] = s
	return nil
}

func (f *fakeRoomStateStore) GetRoom(_ context.Context, roomID string) (ports.RoomSummary, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.rooms[roomID]
	return s, ok, nil
}

func (f *fakeRoomStateStore) ListOpenRooms(context.Context) ([]ports.RoomSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ports.RoomSummary, 0, len(f.rooms))
	for _, s := range f.rooms {
		if s.Status == game.RoomOpen {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeRoomStateStore) RemoveRoom(_ context.Context, roomID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rooms, roomID)
	return nil
}

func (f *fakeRoomStateStore) AcquireJoinLock(_ context.Context, roomID string, _ time.Duration) (func(context.Context) error, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lockFail || f.locks[roomID] {
		return nil, false, nil
	}
	f.locks[roomID] = true
	return func(context.Context) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.locks, roomID)
		return nil
	}, true, nil
}

// fakeSessionStore models the Redis "one room per player" binding with SET NX
// semantics.
type fakeSessionStore struct {
	mu       sync.Mutex
	bindings map[string]string
}

var _ ports.PlayerSessionStore = (*fakeSessionStore)(nil)

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{bindings: map[string]string{}}
}

func (f *fakeSessionStore) BindPlayerToRoom(_ context.Context, playerID, roomID string, _ time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.bindings[playerID]; exists {
		return false, nil
	}
	f.bindings[playerID] = roomID
	return true, nil
}

func (f *fakeSessionStore) ActiveRoom(_ context.Context, playerID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bindings[playerID], nil
}

func (f *fakeSessionStore) ReleasePlayer(_ context.Context, playerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.bindings, playerID)
	return nil
}

func (f *fakeSessionStore) bound(playerID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bindings[playerID]
}

// ---------------------------------------------------------------------------
// misc ports
// ---------------------------------------------------------------------------

type fakeIDGen struct {
	mu     sync.Mutex
	n      int
	prefix string
}

var _ ports.IDGenerator = (*fakeIDGen)(nil)

func (f *fakeIDGen) NewID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	prefix := f.prefix
	if prefix == "" {
		prefix = "id"
	}
	return fmt.Sprintf("%s-%03d", prefix, f.n)
}

// fixedRoller returns the two faces it is told to, so tests decide who wins
// without touching the payout math. Tests mutate it between rounds.
type fixedRoller struct {
	mu   sync.Mutex
	die1 int
	die2 int
	err  error
}

var _ game.DiceRoller = (*fixedRoller)(nil)

func (r *fixedRoller) Roll() (int, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return 0, 0, r.err
	}
	return r.die1, r.die2, nil
}

// set fixes the next roll. even: 2+2=4, odd: 2+1=3.
func (r *fixedRoller) set(die1, die2 int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.die1, r.die2, r.err = die1, die2, nil
}

func (r *fixedRoller) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}
