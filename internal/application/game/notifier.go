package game

// Notifier pushes server-initiated events to players. The WebSocket hub
// implements it; the use cases and the round engine call it without knowing
// that sockets, JSON or connections exist. Implementations must not block:
// the round engine calls these from the goroutine that owns a room's timing.
//
// Only server-driven moments live here. Request/response events (a bet
// acknowledgement, a room.state after joining) are written straight back to
// the caller by the delivery layer.
type Notifier interface {
	// RoundStarted announces a new betting window to a room.
	RoundStarted(out StartRoundOutput)
	// RoundSettled announces the dice, the winners and each member's new
	// balance. The balance push at round end is the only balance the spec
	// allows clients to learn during play.
	RoundSettled(out SettleRoundOutput)
	// RoomClosed announces a terminal room together with final balances.
	RoomClosed(out CloseRoomOutput)
}

// NopNotifier drops every event. It is the default when no delivery layer is
// attached (unit tests, a REST-only deployment).
type NopNotifier struct{}

var _ Notifier = NopNotifier{}

// RoundStarted implements Notifier.
func (NopNotifier) RoundStarted(StartRoundOutput) {}

// RoundSettled implements Notifier.
func (NopNotifier) RoundSettled(SettleRoundOutput) {}

// RoomClosed implements Notifier.
func (NopNotifier) RoomClosed(CloseRoomOutput) {}
