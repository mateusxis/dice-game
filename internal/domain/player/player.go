// Package player holds the player aggregate: identity, credentials and wallet
// balance. It is pure Go and imports nothing outside the standard library.
//
// Money is represented everywhere as int64 *cents*. Floating point is never
// used for monetary values so that every operation is exact and reproducible.
package player

import (
	"strings"
	"time"
)

// Player is the aggregate root for an account.
type Player struct {
	ID           string
	Email        string
	PasswordHash string
	// Balance is stored in cents and can never be negative.
	Balance   int64
	CreatedAt time.Time
}

// New builds a validated Player. It is the only way to obtain a Player with
// guaranteed invariants from outside the package (rehydration from the
// database uses Rehydrate, which trusts already-persisted state).
func New(id, email, passwordHash string, createdAt time.Time) (*Player, error) {
	normalized, err := NormalizeEmail(email)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(id) == "" {
		return nil, ErrInvalidID
	}
	if strings.TrimSpace(passwordHash) == "" {
		return nil, ErrInvalidPasswordHash
	}
	return &Player{
		ID:           id,
		Email:        normalized,
		PasswordHash: passwordHash,
		Balance:      0,
		CreatedAt:    createdAt.UTC(),
	}, nil
}

// Rehydrate rebuilds a Player from persisted state. It still refuses obviously
// corrupt rows (empty id, negative balance) so a storage bug cannot silently
// propagate into the domain.
func Rehydrate(id, email, passwordHash string, balance int64, createdAt time.Time) (*Player, error) {
	if strings.TrimSpace(id) == "" {
		return nil, ErrInvalidID
	}
	if balance < 0 {
		return nil, ErrNegativeBalance
	}
	return &Player{
		ID:           id,
		Email:        email,
		PasswordHash: passwordHash,
		Balance:      balance,
		CreatedAt:    createdAt.UTC(),
	}, nil
}

// NormalizeEmail trims and lowercases an address and performs a deliberately
// small structural check (exactly one "@", non-empty local part, a dot in the
// domain). Deep RFC-5322 validation is out of scope for this test.
func NormalizeEmail(email string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" {
		return "", ErrInvalidEmail
	}
	at := strings.Index(normalized, "@")
	if at <= 0 || at != strings.LastIndex(normalized, "@") {
		return "", ErrInvalidEmail
	}
	domain := normalized[at+1:]
	if domain == "" || !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", ErrInvalidEmail
	}
	if strings.ContainsAny(normalized, " \t\r\n") {
		return "", ErrInvalidEmail
	}
	return normalized, nil
}

// CanAfford reports whether the balance covers amount.
func (p *Player) CanAfford(amount int64) bool {
	return amount > 0 && p.Balance >= amount
}

// Credit adds amount (cents) to the balance. Used by deposits and payouts.
func (p *Player) Credit(amount int64) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}
	p.Balance += amount
	return nil
}

// Debit removes amount (cents) from the balance. Used by withdrawals and by
// stakes, which are debited immediately when a bet is placed.
func (p *Player) Debit(amount int64) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}
	if p.Balance < amount {
		return ErrInsufficientBalance
	}
	p.Balance -= amount
	return nil
}

// TransactionType enumerates the ledger entry kinds.
type TransactionType string

const (
	// TransactionDeposit credits the wallet from an external source.
	TransactionDeposit TransactionType = "deposit"
	// TransactionWithdraw debits the wallet to an external destination.
	TransactionWithdraw TransactionType = "withdraw"
	// TransactionBet debits the stake at bet placement time.
	TransactionBet TransactionType = "bet"
	// TransactionPayout credits winnings at settlement time.
	TransactionPayout TransactionType = "payout"
)

// Valid reports whether t is one of the known ledger entry kinds.
func (t TransactionType) Valid() bool {
	switch t {
	case TransactionDeposit, TransactionWithdraw, TransactionBet, TransactionPayout:
		return true
	default:
		return false
	}
}

// Transaction is an immutable ledger entry. The sum of all entries for a
// player must always equal that player's balance.
type Transaction struct {
	ID        string
	PlayerID  string
	Type      TransactionType
	Amount    int64
	RoundID   *string
	CreatedAt time.Time
}

// NewTransaction builds a validated ledger entry.
func NewTransaction(id, playerID string, t TransactionType, amount int64, roundID *string, createdAt time.Time) (*Transaction, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(playerID) == "" {
		return nil, ErrInvalidID
	}
	if !t.Valid() {
		return nil, ErrInvalidTransactionType
	}
	if amount <= 0 {
		return nil, ErrInvalidAmount
	}
	return &Transaction{
		ID:        id,
		PlayerID:  playerID,
		Type:      t,
		Amount:    amount,
		RoundID:   roundID,
		CreatedAt: createdAt.UTC(),
	}, nil
}
