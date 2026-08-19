package player

import "errors"

// Typed domain errors. The application layer maps these to transport-level
// responses; the domain never knows about HTTP or WebSocket status codes.
var (
	// ErrInvalidEmail is returned when an e-mail address is empty or malformed.
	ErrInvalidEmail = errors.New("player: invalid email")
	// ErrInvalidPasswordHash is returned when a player is built without a hash.
	ErrInvalidPasswordHash = errors.New("player: invalid password hash")
	// ErrInvalidID is returned when the player identifier is empty.
	ErrInvalidID = errors.New("player: invalid id")
	// ErrInvalidAmount is returned when a monetary amount is not strictly positive.
	ErrInvalidAmount = errors.New("player: amount must be greater than zero")
	// ErrInsufficientBalance is returned when a debit would take the balance below zero.
	ErrInsufficientBalance = errors.New("player: insufficient balance")
	// ErrNegativeBalance is returned when a player is built with a negative balance.
	ErrNegativeBalance = errors.New("player: balance cannot be negative")
	// ErrNotFound is returned by repositories when no player matches the criteria.
	ErrNotFound = errors.New("player: not found")
	// ErrEmailAlreadyUsed is returned when registering an already-registered e-mail.
	ErrEmailAlreadyUsed = errors.New("player: email already registered")
	// ErrInvalidCredentials is returned on a failed login attempt.
	ErrInvalidCredentials = errors.New("player: invalid credentials")
	// ErrInvalidTransactionType is returned for an unknown ledger entry kind.
	ErrInvalidTransactionType = errors.New("player: invalid transaction type")
	// ErrWithdrawalBlocked is returned when a player tries to withdraw while
	// sitting in an active room.
	ErrWithdrawalBlocked = errors.New("player: withdrawals are blocked while in an active room")
)
