package ws_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gameapp "github.com/mateusxis/cassino/internal/application/game"
	"github.com/mateusxis/cassino/internal/domain/game"
	"github.com/mateusxis/cassino/internal/domain/player"
	"github.com/mateusxis/cassino/internal/interfaces/ws"
)

// readWait bounds every socket read in these tests; nothing here waits for
// real time on purpose.
const readWait = 2 * time.Second

type env struct {
	server *httptest.Server
	hub    *ws.Hub
	join   *fakeJoin
	state  *fakeRoomState
	rounds *fakeRounds
	audit  *fakeAuditRepo
}

func newEnv(t *testing.T) *env {
	t.Helper()

	join := &fakeJoin{roomID: "room-1"}
	e := &env{
		hub:    ws.NewHub(nil),
		join:   join,
		state:  &fakeRoomState{join: join},
		rounds: &fakeRounds{},
		audit:  &fakeAuditRepo{},
	}
	handler := ws.NewHandler(ws.HandlerOptions{
		Tokens:    fakeTokenService{},
		Hub:       e.hub,
		Join:      e.join,
		State:     e.state,
		Rounds:    e.rounds,
		AuditRepo: e.audit,
		Clock:     fakeClock{now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)},
		IDs:       &fakeIDGen{},
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", handler)
	e.server = httptest.NewServer(mux)
	t.Cleanup(e.server.Close)
	return e
}

// dial opens a socket authenticated with the query parameter, the way Bruno
// and most WebSocket clients do it.
func (e *env) dial(t *testing.T, playerID string) *conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(e.server.URL, "http") + "/ws?token=token-" + playerID
	c, resp, err := websocket.DefaultDialer.Dial(url, nil)
	require.NoError(t, err)
	if resp != nil {
		_ = resp.Body.Close()
	}
	t.Cleanup(func() { _ = c.Close() })
	return &conn{t: t, ws: c}
}

type conn struct {
	t  *testing.T
	ws *websocket.Conn
}

func (c *conn) send(eventType string, data any) {
	c.t.Helper()
	payload := map[string]any{"type": eventType}
	if data != nil {
		payload["data"] = data
	}
	require.NoError(c.t, c.ws.WriteJSON(payload))
}

type frame struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

func (c *conn) read() frame {
	c.t.Helper()
	require.NoError(c.t, c.ws.SetReadDeadline(time.Now().Add(readWait)))
	var f frame
	require.NoError(c.t, c.ws.ReadJSON(&f))
	return f
}

// expect reads until it sees the wanted event, failing on anything unexpected
// after a bounded number of frames.
func (c *conn) expect(eventType string) frame {
	c.t.Helper()
	for i := 0; i < 10; i++ {
		f := c.read()
		if f.Type == eventType {
			return f
		}
	}
	c.t.Fatalf("never received a %q event", eventType)
	return frame{}
}

func decodeInto[T any](t *testing.T, f frame) T {
	t.Helper()
	var out T
	require.NoError(t, json.Unmarshal(f.Data, &out))
	return out
}

// ---------------------------------------------------------------------------
// authentication
// ---------------------------------------------------------------------------

func TestHandshakeRejectsAnInvalidToken(t *testing.T) {
	e := newEnv(t)
	url := "ws" + strings.TrimPrefix(e.server.URL, "http") + "/ws?token=nonsense"

	_, resp, err := websocket.DefaultDialer.Dial(url, nil)
	require.Error(t, err, "a bad token must not get a socket")
	require.NotNil(t, resp)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	entries := e.audit.find("ws.connect")
	require.Len(t, entries, 1, "even a refused handshake is audited")
	assert.False(t, entries[0].Succeeded())
}

func TestHandshakeAcceptsBearerHeader(t *testing.T) {
	e := newEnv(t)
	url := "ws" + strings.TrimPrefix(e.server.URL, "http") + "/ws"

	c, resp, err := websocket.DefaultDialer.Dial(url, http.Header{"Authorization": []string{"Bearer token-alice"}})
	require.NoError(t, err)
	if resp != nil {
		_ = resp.Body.Close()
	}
	defer func() { _ = c.Close() }()

	entries := e.audit.find("ws.connect")
	require.Len(t, entries, 1)
	require.NotNil(t, entries[0].ActorID)
	assert.Equal(t, "alice", *entries[0].ActorID)
}

func TestHandshakeAcceptsAuthAsFirstMessage(t *testing.T) {
	e := newEnv(t)
	url := "ws" + strings.TrimPrefix(e.server.URL, "http") + "/ws"

	raw, resp, err := websocket.DefaultDialer.Dial(url, nil)
	require.NoError(t, err, "a tokenless handshake is upgraded and then asked to authenticate")
	if resp != nil {
		_ = resp.Body.Close()
	}
	defer func() { _ = raw.Close() }()

	c := &conn{t: t, ws: raw}
	c.send(ws.EventAuth, map[string]string{"token": "token-alice"})
	c.send(ws.EventRoomJoin, map[string]string{"room_id": "room-1"})

	state := decodeInto[ws.RoomStatePayload](t, c.expect(ws.EventRoomState))
	assert.Equal(t, "room-1", state.RoomID)
	assert.Equal(t, []string{"alice"}, state.Players)
}

func TestTokenlessSocketIsClosedWithAnError(t *testing.T) {
	e := newEnv(t)
	url := "ws" + strings.TrimPrefix(e.server.URL, "http") + "/ws"

	raw, resp, err := websocket.DefaultDialer.Dial(url, nil)
	require.NoError(t, err)
	if resp != nil {
		_ = resp.Body.Close()
	}
	defer func() { _ = raw.Close() }()

	c := &conn{t: t, ws: raw}
	// Anything other than a valid auth message ends the connection.
	c.send(ws.EventRoomJoin, map[string]string{"room_id": "room-1"})

	errPayload := decodeInto[ws.ErrorPayload](t, c.expect(ws.EventError))
	assert.Equal(t, "unauthorized", errPayload.Code)

	require.NoError(t, raw.SetReadDeadline(time.Now().Add(readWait)))
	_, _, err = raw.ReadMessage()
	assert.Error(t, err, "the socket is closed after a failed authentication")
}

// ---------------------------------------------------------------------------
// protocol
// ---------------------------------------------------------------------------

func TestJoinBroadcastsRoomStateToEveryMember(t *testing.T) {
	e := newEnv(t)

	alice := e.dial(t, "alice")
	alice.send(ws.EventRoomJoin, map[string]string{"room_id": "room-1"})
	first := decodeInto[ws.RoomStatePayload](t, alice.expect(ws.EventRoomState))
	assert.Equal(t, []string{"alice"}, first.Players)

	bob := e.dial(t, "bob")
	bob.send(ws.EventRoomJoin, map[string]string{"room_id": "room-1"})

	// Both sockets see the updated snapshot: the room is a shared table.
	forBob := decodeInto[ws.RoomStatePayload](t, bob.expect(ws.EventRoomState))
	forAlice := decodeInto[ws.RoomStatePayload](t, alice.expect(ws.EventRoomState))
	assert.ElementsMatch(t, []string{"alice", "bob"}, forBob.Players)
	assert.ElementsMatch(t, []string{"alice", "bob"}, forAlice.Players)
	assert.Equal(t, 2, forAlice.PlayerCount)
}

func TestJoinFailureIsReportedAsAnErrorEvent(t *testing.T) {
	e := newEnv(t)
	e.join.err = game.ErrRoomFull

	alice := e.dial(t, "alice")
	alice.send(ws.EventRoomJoin, map[string]string{"room_id": "room-1"})

	payload := decodeInto[ws.ErrorPayload](t, alice.expect(ws.EventError))
	assert.Equal(t, "room_full", payload.Code)
	assert.Equal(t, ws.EventRoomJoin, payload.Event)

	entries := e.audit.find(ws.EventRoomJoin)
	require.Len(t, entries, 1)
	require.NotNil(t, entries[0].Error)
	assert.Contains(t, *entries[0].Error, "room is full")
	assert.JSONEq(t, `{"room_id":"room-1"}`, string(entries[0].Payload), "the event payload is audited")
}

func TestRoundStartRequiresARoom(t *testing.T) {
	e := newEnv(t)

	alice := e.dial(t, "alice")
	alice.send(ws.EventRoundStart, nil)

	payload := decodeInto[ws.ErrorPayload](t, alice.expect(ws.EventError))
	assert.Equal(t, "not_in_room", payload.Code)
}

func TestRoundStartAndBetFlow(t *testing.T) {
	e := newEnv(t)
	closesAt := time.Date(2026, 3, 1, 12, 0, 15, 0, time.UTC)
	e.rounds.startOut = gameapp.StartRoundOutput{
		RoomID: "room-1", RoundID: "round-1", RoundNumber: 1,
		OpensAt: closesAt.Add(-15 * time.Second), ClosesAt: closesAt,
		Members: []string{"alice"},
	}
	e.rounds.betOut = gameapp.PlaceBetOutput{BetID: "bet-1", RoundID: "round-1", RoundNumber: 1}

	alice := e.dial(t, "alice")
	alice.send(ws.EventRoomJoin, map[string]string{"room_id": "room-1"})
	alice.expect(ws.EventRoomState)

	// room.start is an alias of round.start, so both drive the same use case.
	alice.send(ws.EventRoomStart, nil)
	// The engine (not the handler) pushes round.started; here the fake engine
	// does not notify, so the test asserts on the call instead.
	require.Eventually(t, func() bool {
		e.rounds.mu.Lock()
		defer e.rounds.mu.Unlock()
		return len(e.rounds.startCalls) == 1
	}, readWait, 5*time.Millisecond)

	alice.send(ws.EventBetPlace, map[string]any{"choice": "even", "amount": 2500})
	accepted := decodeInto[ws.BetAcceptedPayload](t, alice.expect(ws.EventBetAccepted))

	assert.Equal(t, "bet-1", accepted.BetID)
	assert.Equal(t, "room-1", accepted.RoomID)
	assert.Equal(t, "even", accepted.Choice)
	assert.Equal(t, int64(2500), accepted.Amount)

	// The acknowledgement must not leak a balance: the spec allows it only at
	// round end.
	var raw map[string]any
	require.NoError(t, json.Unmarshal(e.audit.find(ws.EventBetPlace)[0].Payload, &raw))
	assert.NotContains(t, raw, "balance")

	bets := e.rounds.bets()
	require.Len(t, bets, 1)
	assert.Equal(t, game.ChoiceEven, bets[0].Choice)
	assert.Equal(t, "alice", bets[0].PlayerID)
	assert.Equal(t, "room-1", bets[0].RoomID, "the room comes from the session, not the client")
}

func TestBetErrorsAreMappedToStableCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"duplicate", game.ErrDuplicateBet, "duplicate_bet"},
		{"window closed", game.ErrBettingClosed, "betting_closed"},
		{"insufficient balance", player.ErrInsufficientBalance, "insufficient_balance"},
		{"no round", game.ErrNoActiveRound, "no_active_round"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.rounds.betErr = tc.err

			alice := e.dial(t, "alice")
			alice.send(ws.EventRoomJoin, map[string]string{"room_id": "room-1"})
			alice.expect(ws.EventRoomState)

			alice.send(ws.EventBetPlace, map[string]any{"choice": "odd", "amount": 100})
			payload := decodeInto[ws.ErrorPayload](t, alice.expect(ws.EventError))
			assert.Equal(t, tc.code, payload.Code)
			assert.Equal(t, ws.EventBetPlace, payload.Event)
		})
	}
}

func TestInvalidChoiceIsRejected(t *testing.T) {
	e := newEnv(t)

	alice := e.dial(t, "alice")
	alice.send(ws.EventRoomJoin, map[string]string{"room_id": "room-1"})
	alice.expect(ws.EventRoomState)

	alice.send(ws.EventBetPlace, map[string]any{"choice": "maybe", "amount": 100})
	payload := decodeInto[ws.ErrorPayload](t, alice.expect(ws.EventError))
	assert.Equal(t, "invalid_request", payload.Code)
	assert.Empty(t, e.rounds.bets(), "an invalid choice never reaches the use case")
}

func TestUnknownEventIsRejected(t *testing.T) {
	e := newEnv(t)
	alice := e.dial(t, "alice")

	alice.send("room.explode", nil)
	payload := decodeInto[ws.ErrorPayload](t, alice.expect(ws.EventError))
	assert.Equal(t, "unknown_event", payload.Code)

	entries := e.audit.find("room.explode")
	require.Len(t, entries, 1, "even an unknown event is audited")
	assert.False(t, entries[0].Succeeded())
}

func TestMalformedFrameIsRejected(t *testing.T) {
	e := newEnv(t)
	alice := e.dial(t, "alice")

	require.NoError(t, alice.ws.WriteMessage(websocket.TextMessage, []byte("not json")))
	payload := decodeInto[ws.ErrorPayload](t, alice.expect(ws.EventError))
	assert.Equal(t, "invalid_request", payload.Code)
}

func TestSecondConnectionReplacesTheFirst(t *testing.T) {
	e := newEnv(t)

	first := e.dial(t, "alice")
	first.send(ws.EventRoomJoin, map[string]string{"room_id": "room-1"})
	first.expect(ws.EventRoomState)

	second := e.dial(t, "alice")

	payload := decodeInto[ws.ErrorPayload](t, first.expect(ws.EventError))
	assert.Equal(t, "connection_replaced", payload.Code)

	require.NoError(t, first.ws.SetReadDeadline(time.Now().Add(readWait)))
	_, _, err := first.ws.ReadMessage()
	assert.Error(t, err, "the displaced socket is closed")

	// The surviving socket still works.
	second.send(ws.EventRoomJoin, map[string]string{"room_id": "room-1"})
	second.expect(ws.EventRoomState)
}

func TestEveryInboundEventIsAudited(t *testing.T) {
	e := newEnv(t)
	alice := e.dial(t, "alice")

	alice.send(ws.EventRoomJoin, map[string]string{"room_id": "room-1"})
	alice.expect(ws.EventRoomState)

	require.Eventually(t, func() bool { return len(e.audit.find(ws.EventRoomJoin)) == 1 }, readWait, 5*time.Millisecond)

	entry := e.audit.find(ws.EventRoomJoin)[0]
	assert.Equal(t, "ws", string(entry.Channel))
	assert.Equal(t, ws.EventRoomJoin, entry.Action)
	assert.Nil(t, entry.HTTPMethod, "there is no HTTP method on the socket channel")
	require.NotNil(t, entry.ActorID)
	assert.Equal(t, "alice", *entry.ActorID)
	assert.True(t, entry.Succeeded())
}
