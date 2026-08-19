// Package clock provides the authoritative time source. The backend clock —
// not any client-supplied timestamp — decides when a betting window opens and
// closes, so every deadline in the system flows through this port.
package clock

import (
	"sync"
	"time"

	"github.com/mateusxis/cassino/internal/application/ports"
)

// System is the production clock. It always reports UTC so persisted times and
// in-memory comparisons never disagree about a time zone.
type System struct{}

var _ ports.Clock = System{}

// NewSystem returns the production clock.
func NewSystem() System { return System{} }

// Now returns the current UTC time.
func (System) Now() time.Time { return time.Now().UTC() }

// Fake is a controllable clock for tests. Its zero value is not usable; build
// it with NewFake.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

var _ ports.Clock = (*Fake)(nil)

// NewFake returns a clock frozen at t.
func NewFake(t time.Time) *Fake { return &Fake{now: t.UTC()} }

// Now returns the frozen time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance moves the fake clock forward by d.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// Set moves the fake clock to t.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t.UTC()
}
