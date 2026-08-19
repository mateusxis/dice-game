// Package ports declares the interfaces the application layer depends on.
// Infrastructure adapters (PostgreSQL, Redis, JWT, bcrypt, clocks) implement
// them; cmd/api wires concrete types in. Nothing here knows about SQL, HTTP or
// any specific vendor.
package ports

import (
	"context"
	"time"

	"github.com/mateusxis/cassino/internal/domain/audit"
	"github.com/mateusxis/cassino/internal/domain/game"
	"github.com/mateusxis/cassino/internal/domain/player"
)

// ---------------------------------------------------------------------------
// Transactions
// ---------------------------------------------------------------------------

// TxManager runs a function inside a single database transaction. The
// transaction handle is carried on the context, so repositories obtained from
// the same manager automatically enlist in it. fn returning an error rolls the
// transaction back.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// ---------------------------------------------------------------------------
// Player / wallet
// ---------------------------------------------------------------------------

// PlayerRepository persists player accounts and their wallet ledger.
type PlayerRepository interface {
	// Create inserts a new player. It returns player.ErrEmailAlreadyUsed when
	// the e-mail is taken.
	Create(ctx context.Context, p *player.Player) error
	// GetByID returns a player, or player.ErrNotFound.
	GetByID(ctx context.Context, id string) (*player.Player, error)
	// GetByEmail returns a player by normalized e-mail, or player.ErrNotFound.
	GetByEmail(ctx context.Context, email string) (*player.Player, error)
	// GetForUpdate returns a player with the row locked (SELECT ... FOR
	// UPDATE). It must be called inside a TxManager transaction and is the
	// entry point for every balance mutation.
	GetForUpdate(ctx context.Context, id string) (*player.Player, error)
	// UpdateBalance writes an absolute balance for a locked player row.
	UpdateBalance(ctx context.Context, id string, balance int64) error
}

// TransactionRepository appends to the immutable wallet ledger.
type TransactionRepository interface {
	Insert(ctx context.Context, t *player.Transaction) error
	ListByPlayer(ctx context.Context, playerID string, limit, offset int32) ([]*player.Transaction, error)
}

// ---------------------------------------------------------------------------
// Game
// ---------------------------------------------------------------------------

// RoomSummary is a lightweight projection used by the "list open rooms"
// endpoint, which must not load full aggregates.
type RoomSummary struct {
	ID           string
	OwnerID      string
	Status       game.RoomStatus
	CurrentRound int
	MaxRounds    int
	PlayerCount  int
	MaxPlayers   int
	CreatedAt    time.Time
}

// RoomRepository persists rooms and their membership.
type RoomRepository interface {
	Create(ctx context.Context, r *game.Room) error
	// GetByID loads a room aggregate including its seat set. It returns
	// game.ErrRoomNotFound when there is no such room.
	GetByID(ctx context.Context, id string) (*game.Room, error)
	// GetForUpdate loads a room with the row locked, for lifecycle changes.
	GetForUpdate(ctx context.Context, id string) (*game.Room, error)
	// ListOpen returns rooms currently accepting players.
	ListOpen(ctx context.Context, limit, offset int32) ([]RoomSummary, error)
	// UpdateStatus persists a lifecycle transition (and closed_at).
	UpdateStatus(ctx context.Context, id string, status game.RoomStatus, closedAt *time.Time) error
	// SetCurrentRound persists the round counter.
	SetCurrentRound(ctx context.Context, id string, currentRound int) error
	// AddPlayer seats a player. It returns game.ErrAlreadyJoined when the
	// membership row already exists.
	AddPlayer(ctx context.Context, roomID, playerID string, joinedAt time.Time) error
	// RemovePlayer unseats a player.
	RemovePlayer(ctx context.Context, roomID, playerID string) error
	// CountPlayers returns the current seat count.
	CountPlayers(ctx context.Context, roomID string) (int, error)
	// FindActiveRoomForPlayer returns the id of the non-closed room the player
	// currently sits in, or "" when they are free to join one.
	FindActiveRoomForPlayer(ctx context.Context, playerID string) (string, error)
}

// RoomRecoveryRepository lists the rooms a previous process left behind. It is
// deliberately a separate, tiny port: only the startup recovery pass needs it,
// and keeping it out of RoomRepository means nothing else has to know that
// crash recovery exists.
type RoomRecoveryRepository interface {
	// ListActiveRoomIDs returns the ids of every room that is not closed,
	// oldest first.
	ListActiveRoomIDs(ctx context.Context) ([]string, error)
}

// RoundRepository persists rounds.
type RoundRepository interface {
	Create(ctx context.Context, r *game.Round) error
	GetByID(ctx context.Context, id string) (*game.Round, error)
	// GetByRoomAndNumber returns one round of a room, or game.ErrRoundNotFound.
	GetByRoomAndNumber(ctx context.Context, roomID string, number int) (*game.Round, error)
	// GetCurrent returns the highest-numbered round of a room.
	GetCurrent(ctx context.Context, roomID string) (*game.Round, error)
	// Settle persists the dice, outcome and settled status of a round.
	Settle(ctx context.Context, r *game.Round) error
	ListByRoom(ctx context.Context, roomID string) ([]*game.Round, error)
}

// BetRepository persists bets.
type BetRepository interface {
	// Insert stores a bet. The (round_id, player_id) unique constraint is what
	// actually defeats concurrent duplicate submissions; implementations must
	// translate the violation into game.ErrDuplicateBet.
	Insert(ctx context.Context, b *game.Bet) error
	GetByRoundAndPlayer(ctx context.Context, roundID, playerID string) (*game.Bet, error)
	ListByRound(ctx context.Context, roundID string) ([]*game.Bet, error)
	// SettleBet persists the won/payout result of a single bet.
	SettleBet(ctx context.Context, betID string, won bool, payout int64) error
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

// AuditRepository appends to the read-only audit trail.
type AuditRepository interface {
	Append(ctx context.Context, e *audit.Entry) error
}

// ---------------------------------------------------------------------------
// Cache / coordination (Redis-backed)
// ---------------------------------------------------------------------------

// RoomStateStore caches open-room state so listing and lookups avoid hitting
// PostgreSQL, which stays the source of truth.
type RoomStateStore interface {
	// SaveRoom upserts a room summary into the open-room cache.
	SaveRoom(ctx context.Context, s RoomSummary, ttl time.Duration) error
	// GetRoom returns a cached summary; found is false on a cache miss.
	GetRoom(ctx context.Context, roomID string) (s RoomSummary, found bool, err error)
	// ListOpenRooms returns every cached open room.
	ListOpenRooms(ctx context.Context) ([]RoomSummary, error)
	// RemoveRoom evicts a room (used when it closes).
	RemoveRoom(ctx context.Context, roomID string) error
	// AcquireJoinLock takes a short-lived mutex around a room's seat count so
	// concurrent joins cannot exceed the 6-player cap. It returns a release
	// function; acquired is false when the lock is held elsewhere.
	AcquireJoinLock(ctx context.Context, roomID string, ttl time.Duration) (release func(context.Context) error, acquired bool, err error)
}

// PlayerSessionStore tracks which room a player currently occupies, enforcing
// "one room at a time" ahead of the database check.
type PlayerSessionStore interface {
	// BindPlayerToRoom claims the player for a room. bound is false when the
	// player is already bound to some room.
	BindPlayerToRoom(ctx context.Context, playerID, roomID string, ttl time.Duration) (bound bool, err error)
	// ActiveRoom returns the room a player is bound to, or "" when free.
	ActiveRoom(ctx context.Context, playerID string) (string, error)
	// ReleasePlayer clears the binding (on leave or room close).
	ReleasePlayer(ctx context.Context, playerID string) error
}

// ---------------------------------------------------------------------------
// Cross-cutting services
// ---------------------------------------------------------------------------

// Clock is the authoritative time source. Every deadline in the system comes
// from here, never from client input; tests substitute a fake.
type Clock interface {
	Now() time.Time
}

// Claims is the authenticated identity extracted from a token.
type Claims struct {
	PlayerID  string
	Email     string
	ExpiresAt time.Time
}

// TokenService issues and verifies access tokens.
type TokenService interface {
	Issue(playerID, email string) (token string, expiresAt time.Time, err error)
	Verify(token string) (Claims, error)
}

// PasswordHasher hashes and checks account passwords.
type PasswordHasher interface {
	Hash(plain string) (string, error)
	Compare(hash, plain string) error
}

// IDGenerator produces the UUIDs used as primary keys when the application
// (rather than the database default) assigns them.
type IDGenerator interface {
	NewID() string
}
