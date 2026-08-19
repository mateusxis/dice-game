// Package ws holds the WebSocket delivery layer. Phase 1 provides only the
// connection upgrader and its tuning constants; the hub, event router and room
// channels are built on top of them in the game-engine phase.
package ws

import (
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// Connection tuning. The read deadline is longer than the ping period so a
// client that answers pings promptly is never disconnected, while a client
// that goes silent is reaped within one PongWait.
const (
	// WriteWait bounds a single write to a peer.
	WriteWait = 10 * time.Second
	// PongWait is how long we tolerate silence from a peer.
	PongWait = 60 * time.Second
	// PingPeriod is how often we ping an idle peer. It must be shorter than
	// PongWait so a healthy connection always refreshes its deadline in time.
	PingPeriod = (PongWait * 9) / 10
	// MaxMessageSize caps an inbound frame. Game events are tiny JSON objects;
	// anything larger is a client bug or an attack.
	MaxMessageSize = 8 * 1024
)

// NewUpgrader builds the HTTP-to-WebSocket upgrader.
//
// checkOrigin decides which Origin headers may open a socket. The v1 client is
// Bruno / a local tool rather than a browser, so passing nil accepts any
// origin; a browser-facing deployment must pass a real allow-list, since the
// browser same-origin policy does not apply to WebSocket handshakes.
func NewUpgrader(checkOrigin func(*http.Request) bool) *websocket.Upgrader {
	if checkOrigin == nil {
		checkOrigin = func(*http.Request) bool { return true }
	}
	return &websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     checkOrigin,
	}
}

// AllowOrigins returns a CheckOrigin function accepting only the listed
// origins. An empty list rejects every cross-origin handshake.
func AllowOrigins(allowed ...string) func(*http.Request) bool {
	set := make(map[string]struct{}, len(allowed))
	for _, o := range allowed {
		set[o] = struct{}{}
	}
	return func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			// Non-browser clients omit Origin entirely.
			return true
		}
		_, ok := set[origin]
		return ok
	}
}
