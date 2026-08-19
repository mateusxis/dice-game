package ws_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	gameapp "github.com/mateusxis/cassino/internal/application/game"
	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/audit"
	"github.com/mateusxis/cassino/internal/domain/game"
)

// These fakes stand in for the *use cases*, not the repositories: the socket
// layer's job is authentication, event routing, fan-out and auditing, and the
// use case behaviour behind it is already covered in
// internal/application/game.

// fakeTokenService accepts tokens of the form "token-<playerID>".
type fakeTokenService struct{}

var _ ports.TokenService = fakeTokenService{}

func (fakeTokenService) Issue(playerID, email string) (string, time.Time, error) {
	return "token-" + playerID, time.Now().Add(time.Hour), nil
}

func (fakeTokenService) Verify(token string) (ports.Claims, error) {
	const prefix = "token-"
	if len(token) <= len(prefix) || token[:len(prefix)] != prefix {
		return ports.Claims{}, errors.New("invalid token")
	}
	playerID := token[len(prefix):]
	return ports.Claims{PlayerID: playerID, Email: playerID + "@example.test", ExpiresAt: time.Now().Add(time.Hour)}, nil
}

// fakeJoin records join calls and answers from a scripted room.
type fakeJoin struct {
	mu      sync.Mutex
	roomID  string
	players []string
	err     error
	calls   []gameapp.JoinRoomInput
}

func (f *fakeJoin) Execute(_ context.Context, in gameapp.JoinRoomInput) (gameapp.JoinRoomOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	if f.err != nil {
		return gameapp.JoinRoomOutput{}, f.err
	}
	if !contains(f.players, in.PlayerID) {
		f.players = append(f.players, in.PlayerID)
	}
	return gameapp.JoinRoomOutput{
		Room:    ports.RoomSummary{ID: f.roomID, OwnerID: f.players[0], Status: game.RoomOpen, PlayerCount: len(f.players), MaxPlayers: game.MaxPlayers, MaxRounds: game.MaxRounds},
		Players: append([]string(nil), f.players...),
	}, nil
}

func (f *fakeJoin) seats() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.players...)
}

// fakeRoomState mirrors the state the fake join built.
type fakeRoomState struct {
	join  *fakeJoin
	round *gameapp.RoundState
	err   error
}

func (f *fakeRoomState) Execute(_ context.Context, roomID string) (gameapp.RoomState, error) {
	if f.err != nil {
		return gameapp.RoomState{}, f.err
	}
	players := f.join.seats()
	owner := ""
	if len(players) > 0 {
		owner = players[0]
	}
	return gameapp.RoomState{
		Room: ports.RoomSummary{
			ID: roomID, OwnerID: owner, Status: game.RoomOpen,
			PlayerCount: len(players), MaxPlayers: game.MaxPlayers, MaxRounds: game.MaxRounds,
		},
		Players: players,
		Round:   f.round,
	}, nil
}

// fakeRounds is a scripted RoundController.
type fakeRounds struct {
	mu sync.Mutex

	startOut gameapp.StartRoundOutput
	startErr error
	betOut   gameapp.PlaceBetOutput
	betErr   error
	closeOut gameapp.CloseRoomOutput
	closeErr error

	betCalls   []gameapp.PlaceBetInput
	startCalls []gameapp.StartRoundInput
}

func (f *fakeRounds) StartRound(_ context.Context, in gameapp.StartRoundInput) (gameapp.StartRoundOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls = append(f.startCalls, in)
	if f.startErr != nil {
		return gameapp.StartRoundOutput{}, f.startErr
	}
	return f.startOut, nil
}

func (f *fakeRounds) PlaceBet(_ context.Context, in gameapp.PlaceBetInput) (gameapp.PlaceBetOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.betCalls = append(f.betCalls, in)
	if f.betErr != nil {
		return gameapp.PlaceBetOutput{}, f.betErr
	}
	out := f.betOut
	out.RoomID = in.RoomID
	out.Choice = in.Choice
	out.Amount = in.Amount
	return out, nil
}

func (f *fakeRounds) CloseRoom(_ context.Context, _ gameapp.CloseRoomInput) (gameapp.CloseRoomOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closeErr != nil {
		return gameapp.CloseRoomOutput{}, f.closeErr
	}
	return f.closeOut, nil
}

func (f *fakeRounds) bets() []gameapp.PlaceBetInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gameapp.PlaceBetInput(nil), f.betCalls...)
}

// fakeAuditRepo captures the audit trail the socket writes.
type fakeAuditRepo struct {
	mu      sync.Mutex
	entries []*audit.Entry
}

var _ ports.AuditRepository = (*fakeAuditRepo)(nil)

func (f *fakeAuditRepo) Append(_ context.Context, e *audit.Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	clone := *e
	f.entries = append(f.entries, &clone)
	return nil
}

func (f *fakeAuditRepo) all() []*audit.Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*audit.Entry(nil), f.entries...)
}

// find returns the entries recorded for one event name.
func (f *fakeAuditRepo) find(event string) []*audit.Entry {
	var out []*audit.Entry
	for _, e := range f.all() {
		if e.EndpointOrEvent == event {
			out = append(out, e)
		}
	}
	return out
}

type fakeClock struct{ now time.Time }

var _ ports.Clock = fakeClock{}

func (c fakeClock) Now() time.Time { return c.now }

type fakeIDGen struct {
	mu sync.Mutex
	n  int
}

var _ ports.IDGenerator = (*fakeIDGen)(nil)

func (f *fakeIDGen) NewID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	return fmt.Sprintf("id-%03d", f.n)
}

func contains(ids []string, id string) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}
