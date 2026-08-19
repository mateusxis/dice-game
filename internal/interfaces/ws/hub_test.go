package ws_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gameapp "github.com/mateusxis/cassino/internal/application/game"
	"github.com/mateusxis/cassino/internal/domain/game"
	"github.com/mateusxis/cassino/internal/interfaces/ws"
)

// joinedPair connects two players and seats them in room-1.
func joinedPair(t *testing.T, e *env) (*conn, *conn) {
	t.Helper()
	alice := e.dial(t, "alice")
	alice.send(ws.EventRoomJoin, map[string]string{"room_id": "room-1"})
	alice.expect(ws.EventRoomState)

	bob := e.dial(t, "bob")
	bob.send(ws.EventRoomJoin, map[string]string{"room_id": "room-1"})
	bob.expect(ws.EventRoomState)
	alice.expect(ws.EventRoomState)

	return alice, bob
}

func TestHubRoundStartedReachesEveryMember(t *testing.T) {
	e := newEnv(t)
	alice, bob := joinedPair(t, e)

	e.hub.RoundStarted(gameapp.StartRoundOutput{
		RoomID: "room-1", RoundID: "round-1", RoundNumber: 3,
		Members: []string{"alice", "bob"},
	})

	for _, c := range []*conn{alice, bob} {
		payload := decodeInto[ws.RoundStartedPayload](t, c.expect(ws.EventRoundStarted))
		assert.Equal(t, "round-1", payload.RoundID)
		assert.Equal(t, 3, payload.Number)
	}
}

func TestHubRoundSettledPushesResultThenPerPlayerBalance(t *testing.T) {
	e := newEnv(t)
	alice, bob := joinedPair(t, e)

	won := true
	lost := false
	e.hub.RoundSettled(gameapp.SettleRoundOutput{
		RoomID: "room-1", RoundID: "round-1", RoundNumber: 1,
		Die1: 2, Die2: 2, Sum: 4, Outcome: game.OutcomeEven,
		Results: []game.Result{
			{BetID: "bet-a", PlayerID: "alice", Choice: game.ChoiceEven, Amount: 1_000, Won: won, Payout: 1_920},
			{BetID: "bet-b", PlayerID: "bob", Choice: game.ChoiceOdd, Amount: 1_000, Won: lost, Payout: 0},
		},
		Members:  []string{"alice", "bob"},
		Balances: map[string]int64{"alice": 10_920, "bob": 9_000},
	})

	result := decodeInto[ws.RoundResultPayload](t, alice.expect(ws.EventRoundResult))
	assert.Equal(t, 4, result.Sum)
	assert.Equal(t, "even", result.Outcome)
	require.Len(t, result.Winners, 1, "losers are not listed as winners")
	assert.Equal(t, "alice", result.Winners[0].PlayerID)
	assert.Equal(t, int64(1_920), result.Winners[0].Payout)
	assert.False(t, result.RoomClosed)

	// Each player is told their own balance, and only at round end.
	aliceBalance := decodeInto[ws.BalanceUpdatedPayload](t, alice.expect(ws.EventBalanceUpdated))
	assert.Equal(t, int64(10_920), aliceBalance.Balance)
	assert.Equal(t, gameapp.BalanceReasonRoundEnd, aliceBalance.Reason)

	bob.expect(ws.EventRoundResult)
	bobBalance := decodeInto[ws.BalanceUpdatedPayload](t, bob.expect(ws.EventBalanceUpdated))
	assert.Equal(t, "bob", bobBalance.PlayerID)
	assert.Equal(t, int64(9_000), bobBalance.Balance)
}

func TestHubRoundSettledAnnouncesAClosedRoom(t *testing.T) {
	e := newEnv(t)
	alice, _ := joinedPair(t, e)

	e.hub.RoundSettled(gameapp.SettleRoundOutput{
		RoomID: "room-1", RoundID: "round-10", RoundNumber: 10,
		Die1: 1, Die2: 2, Sum: 3, Outcome: game.OutcomeOdd,
		Members:     []string{"alice", "bob"},
		Balances:    map[string]int64{"alice": 9_000, "bob": 9_000},
		RoomClosed:  true,
		CloseReason: gameapp.ReasonMaxRounds,
	})

	result := decodeInto[ws.RoundResultPayload](t, alice.expect(ws.EventRoundResult))
	assert.True(t, result.RoomClosed)

	balance := decodeInto[ws.BalanceUpdatedPayload](t, alice.expect(ws.EventBalanceUpdated))
	assert.Equal(t, gameapp.BalanceReasonRoomEnd, balance.Reason, "the last push of a table is a room-end balance")

	closed := decodeInto[ws.RoomClosedPayload](t, alice.expect(ws.EventRoomClosed))
	assert.Equal(t, "room-1", closed.RoomID)
	assert.Equal(t, gameapp.ReasonMaxRounds, closed.Reason)
}

func TestHubRoomClosedPushesFinalBalances(t *testing.T) {
	e := newEnv(t)
	alice, _ := joinedPair(t, e)

	e.hub.RoomClosed(gameapp.CloseRoomOutput{
		RoomID: "room-1", Status: game.RoomClosed, Closed: true,
		Reason: gameapp.ReasonOwnerClosed, Members: []string{"alice", "bob"},
		Balances: map[string]int64{"alice": 12_000, "bob": 8_000},
	})

	balance := decodeInto[ws.BalanceUpdatedPayload](t, alice.expect(ws.EventBalanceUpdated))
	assert.Equal(t, int64(12_000), balance.Balance)
	assert.Equal(t, gameapp.BalanceReasonRoomEnd, balance.Reason)

	closed := decodeInto[ws.RoomClosedPayload](t, alice.expect(ws.EventRoomClosed))
	assert.Equal(t, gameapp.ReasonOwnerClosed, closed.Reason)
}

func TestHubIgnoresAPendingClose(t *testing.T) {
	e := newEnv(t)
	alice, _ := joinedPair(t, e)

	// closed=false means the room is only "closing": nothing is announced yet,
	// because the live round still has to finish.
	e.hub.RoomClosed(gameapp.CloseRoomOutput{
		RoomID: "room-1", Status: game.RoomClosing, Closed: false,
		Members: []string{"alice", "bob"},
	})
	e.hub.RoundStarted(gameapp.StartRoundOutput{RoomID: "room-1", RoundID: "round-2", RoundNumber: 2, Members: []string{"alice"}})

	// The next frame is the round.started, proving nothing was pushed for the
	// pending close.
	f := alice.read()
	assert.Equal(t, ws.EventRoundStarted, f.Type)
}
