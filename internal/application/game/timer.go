package game

import "time"

// Timer is a single-shot alarm owned by a room's goroutine.
type Timer interface {
	// C delivers exactly one value when the alarm fires.
	C() <-chan time.Time
	// Stop cancels a pending alarm. It is safe to call more than once and
	// after the timer already fired.
	Stop()
}

// TimerFactory creates alarms. Production uses SystemTimers; tests substitute
// a manual factory so a ten-round table settles instantly instead of taking
// 150 real seconds.
//
// The clock and the timers are separate ports on purpose: ports.Clock decides
// *what time it is* (and therefore whether a bet is inside the window), the
// timer only decides *when the engine wakes up*. A test can fire a timer early
// without ever making the clock lie.
type TimerFactory interface {
	NewTimer(d time.Duration) Timer
}

// SystemTimers returns the production timer factory, backed by time.Timer.
func SystemTimers() TimerFactory { return systemTimers{} }

type systemTimers struct{}

func (systemTimers) NewTimer(d time.Duration) Timer {
	if d < 0 {
		d = 0
	}
	return &systemTimer{t: time.NewTimer(d)}
}

type systemTimer struct{ t *time.Timer }

func (s *systemTimer) C() <-chan time.Time { return s.t.C }

func (s *systemTimer) Stop() { s.t.Stop() }
