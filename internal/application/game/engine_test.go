package game_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gameapp "github.com/mateusxis/cassino/internal/application/game"
	"github.com/mateusxis/cassino/internal/domain/game"
	"github.com/mateusxis/cassino/internal/domain/player"
)

// eventWait bounds how long a test waits for the room loop to publish an
// event. Nothing here sleeps for real time; this is only a deadlock guard.
const eventWait = 2 * time.Second

// ---------------------------------------------------------------------------
// fake timers: the betting window fires when the test says so
// ---------------------------------------------------------------------------

type fakeTimer struct {
	c        chan time.Time
	duration time.Duration
	stopped  bool
	mu       sync.Mutex
}

func (t *fakeTimer) C() <-chan time.Time { return t.c }

func (t *fakeTimer) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopped = true
}

func (t *fakeTimer) fire(now time.Time) {
	select {
	case t.c <- now:
	default:
	}
}

type fakeTimers struct {
	mu      sync.Mutex
	timers  []*fakeTimer
	created chan struct{}
}

var _ gameapp.TimerFactory = (*fakeTimers)(nil)

func newFakeTimers() *fakeTimers {
	return &fakeTimers{created: make(chan struct{}, 64)}
}

func (f *fakeTimers) NewTimer(d time.Duration) gameapp.Timer {
	timer := &fakeTimer{c: make(chan time.Time, 1), duration: d}
	f.mu.Lock()
	f.timers = append(f.timers, timer)
	f.mu.Unlock()
	select {
	case f.created <- struct{}{}:
	default:
	}
	return timer
}

func (f *fakeTimers) last() *fakeTimer {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.timers) == 0 {
		return nil
	}
	return f.timers[len(f.timers)-1]
}

func (f *fakeTimers) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.timers)
}

// ---------------------------------------------------------------------------
// notifier recorder
// ---------------------------------------------------------------------------

type recordingNotifier struct {
	started chan gameapp.StartRoundOutput
	settled chan gameapp.SettleRoundOutput
	closed  chan gameapp.CloseRoomOutput
}

var _ gameapp.Notifier = (*recordingNotifier)(nil)

func newRecordingNotifier() *recordingNotifier {
	return &recordingNotifier{
		started: make(chan gameapp.StartRoundOutput, 32),
		settled: make(chan gameapp.SettleRoundOutput, 32),
		closed:  make(chan gameapp.CloseRoomOutput, 32),
	}
}

func (n *recordingNotifier) RoundStarted(out gameapp.StartRoundOutput)  { n.started <- out }
func (n *recordingNotifier) RoundSettled(out gameapp.SettleRoundOutput) { n.settled <- out }
func (n *recordingNotifier) RoomClosed(out gameapp.CloseRoomOutput)     { n.closed <- out }

func (n *recordingNotifier) awaitSettled(t *testing.T) gameapp.SettleRoundOutput {
	t.Helper()
	select {
	case out := <-n.settled:
		return out
	case <-time.After(eventWait):
		t.Fatal("timed out waiting for a round.result notification")
		return gameapp.SettleRoundOutput{}
	}
}

func (n *recordingNotifier) awaitClosed(t *testing.T) gameapp.CloseRoomOutput {
	t.Helper()
	select {
	case out := <-n.closed:
		return out
	case <-time.After(eventWait):
		t.Fatal("timed out waiting for a room.closed notification")
		return gameapp.CloseRoomOutput{}
	}
}

// ---------------------------------------------------------------------------
// engine harness
// ---------------------------------------------------------------------------

type engineHarness struct {
	*harness
	engine   *gameapp.Engine
	timers   *fakeTimers
	notifier *recordingNotifier
}

func newEngineHarness(t *testing.T) *engineHarness {
	t.Helper()
	h := newHarness(t)
	timers := newFakeTimers()
	notifier := newRecordingNotifier()

	engine := gameapp.NewEngine(gameapp.EngineOptions{
		StartRound:  h.startRound,
		PlaceBet:    h.placeBet,
		SettleRound: h.settle,
		CloseRoom:   h.closeRoom,
		AbortRoom:   h.abort,
		Notifier:    notifier,
		Clock:       h.clock,
		Timers:      timers,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), eventWait)
		defer cancel()
		engine.Shutdown(ctx)
	})

	return &engineHarness{harness: h, engine: engine, timers: timers, notifier: notifier}
}

// expire advances the fake clock past the betting window and fires the alarm
// the room loop is waiting on — the test's stand-in for 15 real seconds.
func (h *engineHarness) expire(t *testing.T) {
	t.Helper()
	h.clock.Advance(testWindow)
	timer := h.timers.last()
	require.NotNil(t, timer, "no betting window is armed")
	timer.fire(h.clock.Now())
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

func TestEngineRunsAFullRoundAndPushesResults(t *testing.T) {
	h := newEngineHarness(t)
	h.store.seedRoom("room-1", "owner", "bob")
	h.seatPlayer("owner", 10_000, "room-1")
	h.seatPlayer("bob", 10_000, "room-1")
	h.roller.set(1, 2) // sum 3 -> odd

	started, err := h.engine.StartRound(context.Background(), gameapp.StartRoundInput{RoomID: "room-1", RequesterID: "owner"})
	require.NoError(t, err)
	assert.Equal(t, 1, started.RoundNumber)
	assert.Equal(t, 1, h.timers.count(), "starting a round arms exactly one window")
	assert.Equal(t, testWindow, h.timers.last().duration, "the window lasts BETTING_WINDOW")

	_, err = h.engine.PlaceBet(context.Background(), gameapp.PlaceBetInput{
		RoomID: "room-1", PlayerID: "bob", Choice: game.ChoiceOdd, Amount: 1_000,
	})
	require.NoError(t, err)
	_, err = h.engine.PlaceBet(context.Background(), gameapp.PlaceBetInput{
		RoomID: "room-1", PlayerID: "owner", Choice: game.ChoiceEven, Amount: 1_000,
	})
	require.NoError(t, err)

	h.expire(t)
	out := h.notifier.awaitSettled(t)

	assert.Equal(t, game.OutcomeOdd, out.Outcome)
	require.Len(t, out.Winners(), 1)
	assert.Equal(t, "bob", out.Winners()[0].PlayerID)
	assert.Equal(t, int64(1_920), out.Winners()[0].Payout)
	assert.False(t, out.RoomClosed)
	assert.Equal(t, map[string]int64{"bob": 10_920, "owner": 9_000}, out.Balances)
	assert.Equal(t, 1, h.engine.Rooms(), "the room stays alive between rounds")
}

// After settlement there is no live round at all, so a late bet is refused
// with ErrNoActiveRound; a bet that arrives after closes_at but before the
// engine settles is refused with ErrBettingClosed (see
// TestPlaceBetRejectedAfterTheWindowCloses).
func TestEngineBetAfterSettlementIsRejected(t *testing.T) {
	h := newEngineHarness(t)
	h.store.seedRoom("room-1", "owner")
	h.seatPlayer("owner", 10_000, "room-1")

	_, err := h.engine.StartRound(context.Background(), gameapp.StartRoundInput{RoomID: "room-1", RequesterID: "owner"})
	require.NoError(t, err)

	h.expire(t)
	h.notifier.awaitSettled(t)

	_, err = h.engine.PlaceBet(context.Background(), gameapp.PlaceBetInput{
		RoomID: "room-1", PlayerID: "owner", Choice: game.ChoiceEven, Amount: 1_000,
	})
	assert.ErrorIs(t, err, game.ErrNoActiveRound)
	assert.Equal(t, int64(10_000), h.store.balance("owner"), "a rejected bet never touches the balance")
}

func TestEnginePlaysTenRoundsThenClosesTheRoom(t *testing.T) {
	h := newEngineHarness(t)
	h.store.seedRoom("room-1", "owner")
	h.seatPlayer("owner", 100_000, "room-1")

	for round := 1; round <= game.MaxRounds; round++ {
		started, err := h.engine.StartRound(context.Background(), gameapp.StartRoundInput{RoomID: "room-1", RequesterID: "owner"})
		require.NoError(t, err, "round %d", round)
		require.Equal(t, round, started.RoundNumber)

		h.expire(t)
		out := h.notifier.awaitSettled(t)
		assert.Equal(t, round == game.MaxRounds, out.RoomClosed, "only the tenth round ends the table")
	}

	assert.Equal(t, game.RoomClosed, h.store.roomStatus("room-1"))
	assert.Empty(t, h.sessions.bound("owner"), "the owner is free to withdraw again")

	require.Eventually(t, func() bool { return h.engine.Rooms() == 0 }, eventWait, 5*time.Millisecond,
		"the room loop exits once the room is closed")
}

func TestEngineCloseRequestMidRoundSettlesFirst(t *testing.T) {
	h := newEngineHarness(t)
	h.store.seedRoom("room-1", "owner", "bob")
	h.seatPlayer("owner", 10_000, "room-1")
	h.seatPlayer("bob", 10_000, "room-1")
	h.roller.set(2, 2) // even

	_, err := h.engine.StartRound(context.Background(), gameapp.StartRoundInput{RoomID: "room-1", RequesterID: "owner"})
	require.NoError(t, err)
	_, err = h.engine.PlaceBet(context.Background(), gameapp.PlaceBetInput{
		RoomID: "room-1", PlayerID: "bob", Choice: game.ChoiceEven, Amount: 1_000,
	})
	require.NoError(t, err)

	closed, err := h.engine.CloseRoom(context.Background(), gameapp.CloseRoomInput{RoomID: "room-1", RequesterID: "owner"})
	require.NoError(t, err)
	assert.False(t, closed.Closed, "the close waits for the round")
	assert.Equal(t, game.RoomClosing, closed.Status)

	h.expire(t)
	out := h.notifier.awaitSettled(t)

	assert.True(t, out.RoomClosed, "the room closes as part of the settlement")
	assert.Equal(t, gameapp.ReasonOwnerClosed, out.CloseReason)
	assert.Equal(t, int64(10_920), h.store.balance("bob"), "the pending bet was still paid out")
	assert.Equal(t, game.RoomClosed, h.store.roomStatus("room-1"))
}

func TestEngineCloseOfAnIdleRoomIsImmediate(t *testing.T) {
	h := newEngineHarness(t)
	h.store.seedRoom("room-1", "owner")
	h.seatPlayer("owner", 10_000, "room-1")

	out, err := h.engine.CloseRoom(context.Background(), gameapp.CloseRoomInput{RoomID: "room-1", RequesterID: "owner"})
	require.NoError(t, err)
	assert.True(t, out.Closed)

	announced := h.notifier.awaitClosed(t)
	assert.Equal(t, "room-1", announced.RoomID)
	assert.Equal(t, gameapp.ReasonOwnerClosed, announced.Reason)
	assert.Equal(t, map[string]int64{"owner": 10_000}, announced.Balances)
}

func TestEngineShutdownAbortsALiveRoundAndRefunds(t *testing.T) {
	h := newEngineHarness(t)
	h.store.seedRoom("room-1", "owner")
	h.seatPlayer("owner", 10_000, "room-1")

	_, err := h.engine.StartRound(context.Background(), gameapp.StartRoundInput{RoomID: "room-1", RequesterID: "owner"})
	require.NoError(t, err)
	_, err = h.engine.PlaceBet(context.Background(), gameapp.PlaceBetInput{
		RoomID: "room-1", PlayerID: "owner", Choice: game.ChoiceEven, Amount: 2_000,
	})
	require.NoError(t, err)
	require.Equal(t, int64(8_000), h.store.balance("owner"))

	ctx, cancel := context.WithTimeout(context.Background(), eventWait)
	defer cancel()
	h.engine.Shutdown(ctx)

	out := h.notifier.awaitClosed(t)
	assert.Equal(t, gameapp.ReasonShutdown, out.Reason)
	assert.Equal(t, int64(10_000), h.store.balance("owner"), "an interrupted window refunds every stake")
	require.Len(t, h.store.ledger(player.TransactionPayout), 1)
	assert.Equal(t, game.RoomClosed, h.store.roomStatus("room-1"))
	assert.Zero(t, h.engine.Rooms())
}

func TestEngineRefusesCommandsAfterShutdown(t *testing.T) {
	h := newEngineHarness(t)
	h.store.seedRoom("room-1", "owner")
	h.seatPlayer("owner", 10_000, "room-1")

	ctx, cancel := context.WithTimeout(context.Background(), eventWait)
	defer cancel()
	h.engine.Shutdown(ctx)

	_, err := h.engine.StartRound(context.Background(), gameapp.StartRoundInput{RoomID: "room-1", RequesterID: "owner"})
	assert.ErrorIs(t, err, gameapp.ErrEngineStopped)
}

func TestEngineSerializesConcurrentBets(t *testing.T) {
	h := newEngineHarness(t)
	members := []string{"owner", "p2", "p3", "p4", "p5", "p6"}
	h.store.seedRoom("room-1", members...)
	for _, id := range members {
		h.seatPlayer(id, 10_000, "room-1")
	}
	h.roller.set(2, 2) // even

	_, err := h.engine.StartRound(context.Background(), gameapp.StartRoundInput{RoomID: "room-1", RequesterID: "owner"})
	require.NoError(t, err)

	// Every seat bets at once, and each player also tries a second bet: the
	// unique constraint must reject exactly one per player, whichever lands
	// second.
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		accepted  int
		duplicate int
	)
	for _, id := range members {
		for attempt := 0; attempt < 2; attempt++ {
			wg.Add(1)
			go func(playerID string) {
				defer wg.Done()
				_, err := h.engine.PlaceBet(context.Background(), gameapp.PlaceBetInput{
					RoomID: "room-1", PlayerID: playerID, Choice: game.ChoiceEven, Amount: 1_000,
				})
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					accepted++
				case assert.ErrorIs(t, err, game.ErrDuplicateBet):
					duplicate++
				}
			}(id)
		}
	}
	wg.Wait()

	assert.Equal(t, len(members), accepted, "one bet per player is accepted")
	assert.Equal(t, len(members), duplicate, "every second attempt is rejected as a duplicate")

	h.expire(t)
	out := h.notifier.awaitSettled(t)
	assert.Len(t, out.Winners(), len(members), "all six backed the winning side")
	for _, id := range members {
		assert.Equal(t, int64(10_920), h.store.balance(id), "player %s", id)
	}
}
