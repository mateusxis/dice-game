package ws

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// sendBuffer is how many outbound frames may queue for one connection before
// it is considered too slow. Six players times a handful of events per round
// leaves plenty of headroom; a client that falls this far behind is dropped
// rather than allowed to grow the buffer without bound.
const sendBuffer = 32

// Client is one player's socket. It owns exactly two goroutines — a read pump
// and a write pump — and all writes go through the send channel, because a
// gorilla connection supports only one concurrent writer.
type Client struct {
	hub      *Hub
	conn     *websocket.Conn
	playerID string
	send     chan []byte
	logger   *slog.Logger

	closeOnce sync.Once
	closed    chan struct{}

	mu     sync.Mutex
	roomID string
}

func newClient(hub *Hub, conn *websocket.Conn, playerID string, logger *slog.Logger) *Client {
	return &Client{
		hub:      hub,
		conn:     conn,
		playerID: playerID,
		send:     make(chan []byte, sendBuffer),
		logger:   logger,
		closed:   make(chan struct{}),
	}
}

// PlayerID is the authenticated player behind this socket.
func (c *Client) PlayerID() string { return c.playerID }

// Room returns the room this socket has joined, or "".
func (c *Client) Room() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.roomID
}

func (c *Client) setRoom(roomID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.roomID = roomID
}

// enqueue queues one already-encoded frame. A full queue means the peer is not
// draining; the connection is closed instead of blocking the sender, which
// would otherwise stall the round engine.
func (c *Client) enqueue(payload []byte) {
	select {
	case <-c.closed:
	case c.send <- payload:
	default:
		c.logger.Warn("ws: dropping slow client", "player_id", c.playerID)
		c.close()
	}
}

// send encodes and queues one event.
func (c *Client) sendEvent(eventType string, data any) {
	payload, err := json.Marshal(outbound{Type: eventType, Data: data})
	if err != nil {
		c.logger.Error("ws: failed to encode event", "event", eventType, "error", err)
		return
	}
	c.enqueue(payload)
}

// sendError pushes an error event describing why a command was rejected.
func (c *Client) sendError(event string, err error) {
	code, message := mapError(err)
	c.sendEvent(EventError, ErrorPayload{Code: code, Message: message, Event: event})
}

// close marks the connection terminal exactly once. It deliberately does not
// close the socket itself: the write pump owns that, so anything already
// queued — in particular the error explaining *why* the connection is ending —
// still reaches the peer before the socket goes away.
func (c *Client) close() {
	c.closeOnce.Do(func() { close(c.closed) })
}

// writePump serializes every write to the socket and keeps the peer alive with
// periodic pings. It is the only goroutine that writes to, or closes, the
// underlying connection.
func (c *Client) writePump() {
	ticker := time.NewTicker(PingPeriod)
	defer func() {
		ticker.Stop()
		c.close()
		_ = c.conn.Close()
	}()

	for {
		select {
		case payload := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(WriteWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(WriteWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.closed:
			// Flush whatever is still queued, then send a courtesy close frame.
			// The peer may already be gone, so every write here is best effort.
			c.drain()
			_ = c.conn.SetWriteDeadline(time.Now().Add(WriteWait))
			_ = c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			return
		}
	}
}

// drain writes any frames still queued when the connection is closing.
func (c *Client) drain() {
	for {
		select {
		case payload := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(WriteWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		default:
			return
		}
	}
}

// readPump reads frames until the peer goes away, handing each to handle. A
// peer that neither pongs nor sends anything for PongWait is disconnected.
func (c *Client) readPump(handle func(raw []byte)) {
	defer func() {
		c.hub.unregister(c)
		// Signalling the write pump is what actually closes the socket.
		c.close()
	}()

	c.conn.SetReadLimit(MaxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(PongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(PongWait))
	})

	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				c.logger.Debug("ws: connection ended", "player_id", c.playerID, "error", err)
			}
			return
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(PongWait))
		handle(raw)
	}
}
