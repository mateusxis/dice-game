package ws

import (
	"log/slog"
	"sync"

	gameapp "github.com/mateusxis/cassino/internal/application/game"
)

// Hub is the connection registry and the fan-out point for server pushes.
//
// It holds two indexes:
//
//	clients: playerID -> the one socket that player currently has open;
//	rooms:   roomID   -> the set of players attached to that room.
//
// One connection per player is a deliberate rule: a second login for the same
// account replaces the first, which keeps "who do I push balance.updated to"
// unambiguous and stops a player from watching one table on two sockets.
//
// The hub is the application layer's Notifier: the round engine calls
// RoundStarted / RoundSettled / RoomClosed and the hub turns them into JSON
// frames. It never blocks — a client that cannot keep up is dropped.
type Hub struct {
	logger *slog.Logger

	mu      sync.RWMutex
	clients map[string]*Client
	rooms   map[string]map[string]struct{}
}

var _ gameapp.Notifier = (*Hub)(nil)

// NewHub builds an empty hub.
func NewHub(logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	return &Hub{
		logger:  logger,
		clients: map[string]*Client{},
		rooms:   map[string]map[string]struct{}{},
	}
}

// register adds a client, replacing (and closing) any previous connection the
// same player had open.
func (h *Hub) register(c *Client) {
	h.mu.Lock()
	previous, replaced := h.clients[c.playerID]
	h.clients[c.playerID] = c
	h.mu.Unlock()

	if replaced && previous != c {
		h.logger.Info("ws: replacing existing connection", "player_id", c.playerID)
		previous.sendError("", errReplaced)
		previous.close()
	}
}

// unregister removes a client, but only if it is still the current one for
// that player: a reconnect that already replaced it must not be evicted by the
// old socket's cleanup.
func (h *Hub) unregister(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if current, ok := h.clients[c.playerID]; !ok || current != c {
		return
	}
	delete(h.clients, c.playerID)
	for roomID, members := range h.rooms {
		delete(members, c.playerID)
		if len(members) == 0 {
			delete(h.rooms, roomID)
		}
	}
}

// AttachToRoom records that a player is watching a room. Membership in the
// database is what makes someone a player; this index only decides who gets
// the frames.
func (h *Hub) AttachToRoom(roomID, playerID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	members, ok := h.rooms[roomID]
	if !ok {
		members = map[string]struct{}{}
		h.rooms[roomID] = members
	}
	members[playerID] = struct{}{}
}

// DetachRoom forgets a room entirely and clears the room pointer of every
// socket attached to it. Called when the room closes.
func (h *Hub) DetachRoom(roomID string) {
	h.mu.Lock()
	members := h.rooms[roomID]
	delete(h.rooms, roomID)
	clients := make([]*Client, 0, len(members))
	for playerID := range members {
		if c, ok := h.clients[playerID]; ok {
			clients = append(clients, c)
		}
	}
	h.mu.Unlock()

	for _, c := range clients {
		if c.Room() == roomID {
			c.setRoom("")
		}
	}
}

// SendToPlayer pushes one event to a player, if they are connected.
func (h *Hub) SendToPlayer(playerID, eventType string, data any) {
	h.mu.RLock()
	c := h.clients[playerID]
	h.mu.RUnlock()
	if c == nil {
		return
	}
	c.sendEvent(eventType, data)
}

// SendToPlayers pushes one event to each listed player.
func (h *Hub) SendToPlayers(playerIDs []string, eventType string, data any) {
	for _, id := range playerIDs {
		h.SendToPlayer(id, eventType, data)
	}
}

// Broadcast pushes one event to everyone attached to a room.
func (h *Hub) Broadcast(roomID, eventType string, data any) {
	h.mu.RLock()
	clients := make([]*Client, 0, len(h.rooms[roomID]))
	for playerID := range h.rooms[roomID] {
		if c, ok := h.clients[playerID]; ok {
			clients = append(clients, c)
		}
	}
	h.mu.RUnlock()

	for _, c := range clients {
		c.sendEvent(eventType, data)
	}
}

// ---------------------------------------------------------------------------
// gameapp.Notifier
// ---------------------------------------------------------------------------

// RoundStarted announces a new betting window to the room's members. The
// member list comes from the database rather than the hub index, so a player
// who joined over REST and connected later still gets it.
func (h *Hub) RoundStarted(out gameapp.StartRoundOutput) {
	h.SendToPlayers(out.Members, EventRoundStarted, RoundStartedPayload{
		RoomID:   out.RoomID,
		RoundID:  out.RoundID,
		Number:   out.RoundNumber,
		OpensAt:  out.OpensAt,
		ClosesAt: out.ClosesAt,
	})
}

// RoundSettled pushes the round result to the room and then each member's new
// balance. This ordering matters: a client should learn what happened before
// it is told what it is now worth.
func (h *Hub) RoundSettled(out gameapp.SettleRoundOutput) {
	winners := out.Winners()
	payload := RoundResultPayload{
		RoomID:     out.RoomID,
		RoundID:    out.RoundID,
		Number:     out.RoundNumber,
		Die1:       out.Die1,
		Die2:       out.Die2,
		Sum:        out.Sum,
		Outcome:    string(out.Outcome),
		Winners:    make([]WinnerPayload, 0, len(winners)),
		RoomClosed: out.RoomClosed,
	}
	for _, w := range winners {
		payload.Winners = append(payload.Winners, WinnerPayload{
			PlayerID: w.PlayerID,
			BetID:    w.BetID,
			Choice:   string(w.Choice),
			Amount:   w.Amount,
			Payout:   w.Payout,
		})
	}
	h.SendToPlayers(out.Members, EventRoundResult, payload)

	reason := gameapp.BalanceReasonRoundEnd
	if out.RoomClosed {
		reason = gameapp.BalanceReasonRoomEnd
	}
	h.pushBalances(out.RoomID, out.Members, out.Balances, reason)

	if out.RoomClosed {
		h.SendToPlayers(out.Members, EventRoomClosed, RoomClosedPayload{RoomID: out.RoomID, Reason: out.CloseReason})
		h.DetachRoom(out.RoomID)
	}
}

// RoomClosed announces a terminal room and pushes the final balances.
func (h *Hub) RoomClosed(out gameapp.CloseRoomOutput) {
	if !out.Closed {
		return
	}
	h.pushBalances(out.RoomID, out.Members, out.Balances, gameapp.BalanceReasonRoomEnd)
	h.SendToPlayers(out.Members, EventRoomClosed, RoomClosedPayload{RoomID: out.RoomID, Reason: out.Reason})
	h.DetachRoom(out.RoomID)
}

func (h *Hub) pushBalances(roomID string, members []string, balances map[string]int64, reason string) {
	for _, playerID := range members {
		balance, ok := balances[playerID]
		if !ok {
			continue
		}
		h.SendToPlayer(playerID, EventBalanceUpdated, BalanceUpdatedPayload{
			PlayerID: playerID,
			RoomID:   roomID,
			Balance:  balance,
			Reason:   reason,
		})
	}
}
