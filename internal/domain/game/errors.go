package game

import "errors"

// Typed domain errors for the room/round/bet lifecycle.
var (
	// ErrInvalidID is returned when an identifier is empty.
	ErrInvalidID = errors.New("game: invalid id")
	// ErrRoomFull is returned when a seventh player tries to join.
	ErrRoomFull = errors.New("game: room is full")
	// ErrRoomNotOpen is returned when acting on a closing or closed room.
	ErrRoomNotOpen = errors.New("game: room is not open")
	// ErrRoomClosed is returned when acting on an already-closed room.
	ErrRoomClosed = errors.New("game: room is closed")
	// ErrAlreadyJoined is returned when a player joins a room twice.
	ErrAlreadyJoined = errors.New("game: player already joined this room")
	// ErrNotAMember is returned when a non-member acts inside a room.
	ErrNotAMember = errors.New("game: player is not a member of this room")
	// ErrNotOwner is returned when a non-owner attempts an owner-only action.
	ErrNotOwner = errors.New("game: only the room owner may perform this action")
	// ErrNoPlayers is returned when starting a round in an empty room.
	ErrNoPlayers = errors.New("game: room has no players")
	// ErrMaxRoundsReached is returned when the 10-round cap is hit.
	ErrMaxRoundsReached = errors.New("game: room reached its round limit")
	// ErrRoundInProgress is returned when starting a round while one is live.
	ErrRoundInProgress = errors.New("game: a round is already in progress")
	// ErrNoActiveRound is returned when betting or settling without a live round.
	ErrNoActiveRound = errors.New("game: no active round")
	// ErrRoundAlreadySettled is returned when settling a settled round.
	ErrRoundAlreadySettled = errors.New("game: round already settled")
	// ErrBettingClosed is returned when a bet arrives outside the betting window.
	ErrBettingClosed = errors.New("game: betting window is closed")
	// ErrBettingNotOpen is returned when a bet arrives before the window opens.
	ErrBettingNotOpen = errors.New("game: betting window is not open yet")
	// ErrDuplicateBet is returned when a player bets twice in the same round.
	ErrDuplicateBet = errors.New("game: player already placed a bet in this round")
	// ErrInvalidChoice is returned for a choice other than "even"/"odd".
	ErrInvalidChoice = errors.New("game: bet choice must be even or odd")
	// ErrInvalidAmount is returned when a stake is not strictly positive.
	ErrInvalidAmount = errors.New("game: bet amount must be greater than zero")
	// ErrInvalidDie is returned when a die value falls outside 1..6.
	ErrInvalidDie = errors.New("game: die value must be between 1 and 6")
	// ErrInvalidRoundNumber is returned for a round number outside 1..MaxRounds.
	ErrInvalidRoundNumber = errors.New("game: invalid round number")
	// ErrInvalidBettingWindow is returned when closes_at is not after opens_at.
	ErrInvalidBettingWindow = errors.New("game: betting window must be a positive duration")
	// ErrRoomNotFound is returned by repositories when no room matches.
	ErrRoomNotFound = errors.New("game: room not found")
	// ErrRoundNotFound is returned by repositories when no round matches.
	ErrRoundNotFound = errors.New("game: round not found")
	// ErrBetNotFound is returned by repositories when no bet matches.
	ErrBetNotFound = errors.New("game: bet not found")
	// ErrAlreadyInAnotherRoom is returned when a player joins a second room.
	ErrAlreadyInAnotherRoom = errors.New("game: player is already in another room")
)
