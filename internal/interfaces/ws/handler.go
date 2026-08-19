package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	gameapp "github.com/mateusxis/cassino/internal/application/game"
	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/game"
)

// authDeadline bounds how long a socket that arrived without a token may take
// to send its auth message before it is dropped.
const authDeadline = 10 * time.Second

// bearerPrefix is the Authorization scheme accepted on the handshake.
const bearerPrefix = "Bearer "

// RoomJoiner seats a player in a room (application use case).
type RoomJoiner interface {
	Execute(ctx context.Context, in gameapp.JoinRoomInput) (gameapp.JoinRoomOutput, error)
}

// RoomStateReader builds the room.state snapshot.
type RoomStateReader interface {
	Execute(ctx context.Context, roomID string) (gameapp.RoomState, error)
}

// RoundController is the round engine as far as the socket layer is concerned.
// *gameapp.Engine implements it.
type RoundController interface {
	StartRound(ctx context.Context, in gameapp.StartRoundInput) (gameapp.StartRoundOutput, error)
	PlaceBet(ctx context.Context, in gameapp.PlaceBetInput) (gameapp.PlaceBetOutput, error)
	CloseRoom(ctx context.Context, in gameapp.CloseRoomInput) (gameapp.CloseRoomOutput, error)
}

// HandlerOptions collects the socket endpoint's dependencies.
type HandlerOptions struct {
	// Tokens verifies the same JWT the REST API issues — a socket is not a
	// second authentication scheme.
	Tokens ports.TokenService
	Hub    *Hub
	Join   RoomJoiner
	State  RoomStateReader
	Rounds RoundController

	// AuditRepo, Clock and IDs back the per-event audit trail. A nil repo
	// disables auditing (narrow unit tests only).
	AuditRepo ports.AuditRepository
	Clock     ports.Clock
	IDs       ports.IDGenerator

	Logger   *slog.Logger
	Upgrader *websocket.Upgrader
}

// NewHandler builds the GET /ws endpoint.
//
// Authentication accepts the token three ways, checked in this order:
// the ?token= query parameter (what Bruno and most WebSocket clients can send),
// an Authorization: Bearer header, and finally an {"type":"auth"} first
// message for clients that can set neither. A socket that authenticates none
// of those ways is told why and closed.
func NewHandler(opts HandlerOptions) http.HandlerFunc {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	upgrader := opts.Upgrader
	if upgrader == nil {
		upgrader = NewUpgrader(nil)
	}

	return func(w http.ResponseWriter, r *http.Request) {
		token := handshakeToken(r)

		// A token present but invalid is rejected before the upgrade, so a
		// bad client gets a normal HTTP 401 rather than a socket that closes
		// on it immediately.
		var claims ports.Claims
		if token != "" {
			var err error
			claims, err = opts.Tokens.Verify(token)
			if err != nil {
				writeAudit(r.Context(), opts, nil, "ws.connect", nil, errUnauthenticated)
				http.Error(w, `{"error":{"code":"unauthorized","message":"authentication is required"}}`, http.StatusUnauthorized)
				return
			}
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			// Upgrade already wrote a response.
			logger.Debug("ws: upgrade failed", "error", err)
			return
		}

		if claims.PlayerID == "" {
			claims, err = awaitAuthMessage(conn, opts.Tokens)
			if err != nil {
				writeAudit(r.Context(), opts, nil, "ws.connect", nil, err)
				_ = conn.WriteJSON(outbound{Type: EventError, Data: ErrorPayload{Code: "unauthorized", Message: "authentication is required", Event: EventAuth}})
				_ = conn.Close()
				return
			}
		}

		client := newClient(opts.Hub, conn, claims.PlayerID, logger)
		opts.Hub.register(client)
		writeAudit(r.Context(), opts, &claims.PlayerID, "ws.connect", nil, nil)
		logger.Info("ws: client connected", "player_id", claims.PlayerID)

		s := &session{opts: opts, logger: logger, client: client}
		go client.writePump()
		client.readPump(s.handle)
		logger.Info("ws: client disconnected", "player_id", claims.PlayerID)
	}
}

// handshakeToken extracts the bearer token from the query string or the
// Authorization header.
func handshakeToken(r *http.Request) string {
	if token := strings.TrimSpace(r.URL.Query().Get("token")); token != "" {
		return token
	}
	header := r.Header.Get("Authorization")
	if strings.HasPrefix(header, bearerPrefix) {
		return strings.TrimSpace(strings.TrimPrefix(header, bearerPrefix))
	}
	return ""
}

// awaitAuthMessage reads exactly one frame and expects it to be an auth event.
func awaitAuthMessage(conn *websocket.Conn, tokens ports.TokenService) (ports.Claims, error) {
	conn.SetReadLimit(MaxMessageSize)
	if err := conn.SetReadDeadline(time.Now().Add(authDeadline)); err != nil {
		return ports.Claims{}, err
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return ports.Claims{}, errUnauthenticated
	}
	var msg inbound
	if err := json.Unmarshal(raw, &msg); err != nil || msg.Type != EventAuth {
		return ports.Claims{}, errUnauthenticated
	}
	var data authData
	if err := json.Unmarshal(msg.Data, &data); err != nil || strings.TrimSpace(data.Token) == "" {
		return ports.Claims{}, errUnauthenticated
	}
	claims, err := tokens.Verify(data.Token)
	if err != nil {
		return ports.Claims{}, errUnauthenticated
	}
	return claims, nil
}

// session is the per-connection event router. Its methods run on the read
// pump's goroutine, one message at a time, so it needs no locking of its own.
type session struct {
	opts   HandlerOptions
	logger *slog.Logger
	client *Client
}

// handle routes one inbound frame and audits it, whatever the outcome.
func (s *session) handle(raw []byte) {
	var msg inbound
	if err := json.Unmarshal(raw, &msg); err != nil {
		writeAudit(context.Background(), s.opts, &s.client.playerID, "ws.message", nil, errMalformedMessage)
		s.client.sendError("", errMalformedMessage)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var err error
	switch msg.Type {
	case EventRoomJoin:
		err = s.join(ctx, msg.Data)
	case EventRoomStart, EventRoundStart:
		err = s.startRound(ctx)
	case EventBetPlace:
		err = s.placeBet(ctx, msg.Data)
	case EventAuth:
		// Already authenticated; a repeat auth is a no-op rather than an error.
	default:
		err = errUnknownEvent
	}

	writeAudit(ctx, s.opts, &s.client.playerID, msg.Type, msg.Data, err)
	if err != nil {
		s.client.sendError(msg.Type, err)
	}
}

// join seats the player and pushes the resulting room.state to everyone in the
// room, so the other five see the new arrival.
func (s *session) join(ctx context.Context, data json.RawMessage) error {
	var req joinData
	if err := decode(data, &req); err != nil {
		return err
	}
	if strings.TrimSpace(req.RoomID) == "" {
		return game.ErrInvalidID
	}

	out, err := s.opts.Join.Execute(ctx, gameapp.JoinRoomInput{RoomID: req.RoomID, PlayerID: s.client.playerID})
	if err != nil {
		return err
	}

	s.client.setRoom(out.Room.ID)
	s.opts.Hub.AttachToRoom(out.Room.ID, s.client.playerID)

	state, err := s.opts.State.Execute(ctx, out.Room.ID)
	if err != nil {
		return err
	}
	payload := roomStatePayload(state)
	// Everyone seated gets the new snapshot; the joiner is in that list too.
	s.opts.Hub.SendToPlayers(state.Players, EventRoomState, payload)
	if !containsID(state.Players, s.client.playerID) {
		s.client.sendEvent(EventRoomState, payload)
	}
	return nil
}

// startRound opens the next betting window. The engine enforces owner-only.
func (s *session) startRound(ctx context.Context) error {
	roomID := s.client.Room()
	if roomID == "" {
		return gameapp.ErrNotInRoom
	}
	_, err := s.opts.Rounds.StartRound(ctx, gameapp.StartRoundInput{RoomID: roomID, RequesterID: s.client.playerID})
	// round.started is pushed by the engine through the hub, so nothing is
	// written back here on success.
	return err
}

// placeBet submits a wager and acknowledges it — without a balance, by design.
func (s *session) placeBet(ctx context.Context, data json.RawMessage) error {
	roomID := s.client.Room()
	if roomID == "" {
		return gameapp.ErrNotInRoom
	}
	var req betData
	if err := decode(data, &req); err != nil {
		return err
	}
	choice, err := game.ParseChoice(req.Choice)
	if err != nil {
		return err
	}

	out, err := s.opts.Rounds.PlaceBet(ctx, gameapp.PlaceBetInput{
		RoomID:   roomID,
		PlayerID: s.client.playerID,
		Choice:   choice,
		Amount:   req.Amount,
	})
	if err != nil {
		return err
	}
	s.client.sendEvent(EventBetAccepted, BetAcceptedPayload{
		BetID:   out.BetID,
		RoomID:  out.RoomID,
		RoundID: out.RoundID,
		Number:  out.RoundNumber,
		Choice:  string(out.Choice),
		Amount:  out.Amount,
	})
	return nil
}

func decode(data json.RawMessage, dst any) error {
	if len(data) == 0 {
		return errMalformedMessage
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return errMalformedMessage
	}
	return nil
}

func containsID(ids []string, id string) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

// roomStatePayload projects the application read model onto the wire shape.
func roomStatePayload(state gameapp.RoomState) RoomStatePayload {
	payload := RoomStatePayload{
		RoomID:       state.Room.ID,
		OwnerID:      state.Room.OwnerID,
		Status:       string(state.Room.Status),
		CurrentRound: state.Room.CurrentRound,
		MaxRounds:    state.Room.MaxRounds,
		PlayerCount:  state.Room.PlayerCount,
		MaxPlayers:   state.Room.MaxPlayers,
		Players:      state.Players,
	}
	if payload.Players == nil {
		payload.Players = []string{}
	}
	if state.Round != nil {
		payload.Round = &RoundPayload{
			RoundID:  state.Round.RoundID,
			Number:   state.Round.Number,
			Status:   string(state.Round.Status),
			OpensAt:  state.Round.OpensAt,
			ClosesAt: state.Round.ClosesAt,
			Die1:     state.Round.Die1,
			Die2:     state.Round.Die2,
			Outcome:  string(state.Round.Outcome),
		}
	}
	return payload
}
