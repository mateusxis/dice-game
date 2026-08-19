package game

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/mateusxis/cassino/internal/application/ports"
	"github.com/mateusxis/cassino/internal/domain/game"
)

// defaultOpTimeout bounds a database operation the engine starts on its own
// (settlement, abort). Caller-initiated commands keep the caller's context.
const defaultOpTimeout = 15 * time.Second

// EngineOptions collects everything the round engine needs.
type EngineOptions struct {
	StartRound  *StartRoundUseCase
	PlaceBet    *PlaceBetUseCase
	SettleRound *SettleRoundUseCase
	CloseRoom   *CloseRoomUseCase
	AbortRoom   *AbortRoomUseCase

	// Notifier receives round.started / round.result / room.closed. Nil means
	// NopNotifier (the engine still works, nobody is told).
	Notifier Notifier
	Clock    ports.Clock
	// Timers creates the betting-window alarms. Nil means SystemTimers.
	Timers TimerFactory
	Logger *slog.Logger
	// OpTimeout bounds engine-initiated database work. Zero means 15s.
	OpTimeout time.Duration
}

// Engine owns the *timing* of the game: one goroutine per active room, which
// is the only place a round is started, settled or closed.
//
// Why a goroutine per room
// ------------------------
// A round has a deadline, and something has to be awake to notice it. Giving
// each room its own loop also serializes everything that touches that room's
// round lifecycle, so "start a round while one is live", "settle twice" and
// "bet during settlement" cannot even be attempted concurrently.
//
// Correctness does not *depend* on that serialization: every use case behind
// the engine is a single transaction guarded by row locks and unique
// constraints (one bet per round/player, one round per room/number, settlement
// guarded on status = 'betting'). The loop is what makes the timing coherent
// and the failure modes boring — bets from six sockets arrive as one ordered
// stream instead of six racing transactions.
//
// Lifecycle: a loop is created lazily by the first round.start for a room and
// exits when the room closes, when settlement fails, or on shutdown.
type Engine struct {
	opts     EngineOptions
	notifier Notifier
	timers   TimerFactory
	logger   *slog.Logger
	timeout  time.Duration

	mu      sync.Mutex
	loops   map[string]*roomLoop
	stopped bool
	wg      sync.WaitGroup
}

// NewEngine builds a round engine. It starts no goroutines by itself.
func NewEngine(opts EngineOptions) *Engine {
	e := &Engine{
		opts:     opts,
		notifier: opts.Notifier,
		timers:   opts.Timers,
		logger:   opts.Logger,
		timeout:  opts.OpTimeout,
		loops:    map[string]*roomLoop{},
	}
	if e.notifier == nil {
		e.notifier = NopNotifier{}
	}
	if e.timers == nil {
		e.timers = SystemTimers()
	}
	if e.logger == nil {
		e.logger = slog.Default()
	}
	if e.timeout <= 0 {
		e.timeout = defaultOpTimeout
	}
	return e
}

// StartRound opens the next betting window of a room, arming the timer that
// will settle it. Only the room owner may call it (enforced by the use case).
func (e *Engine) StartRound(ctx context.Context, in StartRoundInput) (StartRoundOutput, error) {
	res, err := e.dispatch(ctx, in.RoomID, &command{kind: cmdStartRound, start: in}, true)
	if err != nil {
		return StartRoundOutput{}, err
	}
	return res.start, res.err
}

// PlaceBet records a bet through the room's loop, so bets are applied in a
// single order and none can slip in while the round is being settled.
func (e *Engine) PlaceBet(ctx context.Context, in PlaceBetInput) (PlaceBetOutput, error) {
	res, err := e.dispatch(ctx, in.RoomID, &command{kind: cmdPlaceBet, bet: in}, false)
	if errors.Is(err, errNoLoop) {
		// No live loop: the room never started a round or has already closed.
		// Running the use case directly returns the accurate domain error
		// (ErrNoActiveRound / ErrRoomClosed) instead of a made-up one.
		return e.opts.PlaceBet.Execute(ctx, in)
	}
	if err != nil {
		return PlaceBetOutput{}, err
	}
	return res.bet, res.err
}

// CloseRoom applies an owner's close request. With a live round the room only
// moves to "closing" here; the loop closes it for real once that round
// settles.
func (e *Engine) CloseRoom(ctx context.Context, in CloseRoomInput) (CloseRoomOutput, error) {
	res, err := e.dispatch(ctx, in.RoomID, &command{kind: cmdCloseRoom, close: in}, false)
	if errors.Is(err, errNoLoop) {
		// Idle room (no round ever started, or all rounds already settled):
		// close it straight away and announce it.
		out, err := e.opts.CloseRoom.Execute(ctx, in)
		if err != nil {
			return CloseRoomOutput{}, err
		}
		if out.Closed {
			e.notifier.RoomClosed(out)
		}
		return out, nil
	}
	if err != nil {
		return CloseRoomOutput{}, err
	}
	return res.close, res.err
}

// Rooms returns how many room loops are currently running (diagnostics/tests).
func (e *Engine) Rooms() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.loops)
}

// Shutdown stops every room loop and waits for them, bounded by ctx.
//
// Shutdown semantics: a room with an open betting window is **aborted, not
// settled** — every stake placed in that window is refunded (ledger "payout"
// entries), the room is closed and its members are unbound and told why.
// Settling early would resolve a window that players were still allowed to bet
// into; refunding is the neutral outcome. Rooms sitting idle between rounds
// are simply left as they are, and the next process closes them in its startup
// recovery pass.
func (e *Engine) Shutdown(ctx context.Context) {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return
	}
	e.stopped = true
	loops := make([]*roomLoop, 0, len(e.loops))
	for _, l := range e.loops {
		loops = append(loops, l)
	}
	e.mu.Unlock()

	for _, l := range loops {
		l.stop()
	}

	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		e.logger.Warn("engine: shutdown timed out with rooms still running", "rooms", e.Rooms())
	}
}

// ---------------------------------------------------------------------------
// command plumbing
// ---------------------------------------------------------------------------

type cmdKind int

const (
	cmdStartRound cmdKind = iota
	cmdPlaceBet
	cmdCloseRoom
)

type command struct {
	kind  cmdKind
	start StartRoundInput
	bet   PlaceBetInput
	close CloseRoomInput
	reply chan commandResult
}

type commandResult struct {
	start StartRoundOutput
	bet   PlaceBetOutput
	close CloseRoomOutput
	err   error
}

// errNoLoop means the room has no running loop; callers decide whether that is
// a failure or a reason to run the use case directly.
var errNoLoop = errors.New("game: room has no running loop")

// dispatch hands a command to a room's loop and waits for its reply.
func (e *Engine) dispatch(ctx context.Context, roomID string, cmd *command, create bool) (commandResult, error) {
	l, err := e.loopFor(roomID, create)
	if err != nil {
		return commandResult{}, err
	}
	cmd.reply = make(chan commandResult, 1)

	select {
	case l.cmds <- cmd:
	case <-l.done:
		return commandResult{}, errNoLoop
	case <-ctx.Done():
		return commandResult{}, ctx.Err()
	}

	select {
	case res := <-cmd.reply:
		return res, nil
	case <-ctx.Done():
		return commandResult{}, ctx.Err()
	case <-l.done:
		// The loop always replies before exiting, so a buffered reply may still
		// be waiting here.
		select {
		case res := <-cmd.reply:
			return res, nil
		default:
			return commandResult{}, errNoLoop
		}
	}
}

// loopFor returns the room's loop, starting one when create is set.
func (e *Engine) loopFor(roomID string, create bool) (*roomLoop, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped {
		return nil, ErrEngineStopped
	}
	if l, ok := e.loops[roomID]; ok {
		return l, nil
	}
	if !create {
		return nil, errNoLoop
	}
	l := &roomLoop{
		engine: e,
		roomID: roomID,
		cmds:   make(chan *command),
		quit:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	e.loops[roomID] = l
	e.wg.Add(1)
	go l.run()
	return l, nil
}

func (e *Engine) removeLoop(l *roomLoop) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if current, ok := e.loops[l.roomID]; ok && current == l {
		delete(e.loops, l.roomID)
	}
}

// ---------------------------------------------------------------------------
// per-room loop
// ---------------------------------------------------------------------------

// roomLoop is the single goroutine owning one room's round lifecycle.
type roomLoop struct {
	engine *Engine
	roomID string

	cmds chan *command
	quit chan struct{}
	done chan struct{}
	once sync.Once

	// Loop-owned state; touched only by run().
	timer Timer
	live  bool
}

func (l *roomLoop) stop() {
	l.once.Do(func() { close(l.quit) })
}

// run is the serialization point for a room: commands, the betting-window
// alarm and shutdown are handled one at a time, in arrival order.
func (l *roomLoop) run() {
	defer func() {
		l.disarm()
		l.engine.removeLoop(l)
		close(l.done)
		l.engine.wg.Done()
	}()

	for {
		select {
		case <-l.quit:
			if l.live {
				l.abort(ReasonShutdown)
			}
			return

		case <-l.alarm():
			l.disarm()
			if closed := l.settle(); closed {
				return
			}

		case cmd := <-l.cmds:
			if done := l.handle(cmd); done {
				return
			}
		}
	}
}

// alarm returns the current betting-window channel, or nil when no round is
// live — a nil channel blocks forever, which is exactly what an idle room
// should do.
func (l *roomLoop) alarm() <-chan time.Time {
	if l.timer == nil {
		return nil
	}
	return l.timer.C()
}

func (l *roomLoop) arm(closesAt time.Time) {
	l.disarm()
	d := closesAt.Sub(l.engine.opts.Clock.Now())
	if d < 0 {
		d = 0
	}
	l.timer = l.engine.timers.NewTimer(d)
	l.live = true
}

func (l *roomLoop) disarm() {
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
}

// handle runs one command and reports whether the loop should exit.
//
// The command deliberately does not carry the caller's context: the loop must
// finish what it started even if the socket that asked for it disconnected
// mid-flight, otherwise a cancelled bet could leave a debit without a bet row.
func (l *roomLoop) handle(cmd *command) bool {
	ctx, cancel := context.WithTimeout(context.Background(), l.engine.timeout)
	defer cancel()

	switch cmd.kind {
	case cmdStartRound:
		out, err := l.engine.opts.StartRound.Execute(ctx, cmd.start)
		if err == nil {
			// Arm and announce before replying, so that by the time the caller
			// sees its answer the window is genuinely counting down.
			l.arm(out.ClosesAt)
			l.engine.notifier.RoundStarted(out)
		}
		cmd.reply <- commandResult{start: out, err: err}
		// A start that never happened leaves the loop idle, waiting for the
		// next attempt.
		return false

	case cmdPlaceBet:
		out, err := l.engine.opts.PlaceBet.Execute(ctx, cmd.bet)
		cmd.reply <- commandResult{bet: out, err: err}
		return false

	case cmdCloseRoom:
		out, err := l.engine.opts.CloseRoom.Execute(ctx, cmd.close)
		if err == nil && out.Closed {
			l.engine.notifier.RoomClosed(out)
		}
		cmd.reply <- commandResult{close: out, err: err}
		// A close that only marked the room "closing" leaves the loop running:
		// the pending round still has to be settled, and settlement is what
		// actually ends the table.
		return err == nil && out.Closed
	}
	return false
}

// settle resolves the round whose window just expired and reports whether the
// room ended with it.
func (l *roomLoop) settle() bool {
	ctx, cancel := context.WithTimeout(context.Background(), l.engine.timeout)
	defer cancel()

	out, err := l.engine.opts.SettleRound.Execute(ctx, SettleRoundInput{RoomID: l.roomID})
	if err != nil {
		l.live = false
		switch {
		case errors.Is(err, game.ErrNoActiveRound), errors.Is(err, game.ErrRoundAlreadySettled):
			// Someone else already resolved it (a retry, another process).
			// Nothing to announce and nothing to clean up.
			return false
		case errors.Is(err, game.ErrRoomClosed), errors.Is(err, game.ErrRoomNotFound):
			return true
		}
		// Settlement itself failed — a dead entropy source, a database
		// outage. The table cannot continue honestly, so it is aborted and
		// every open stake refunded.
		l.engine.logger.Error("engine: settlement failed, aborting room", "room_id", l.roomID, "error", err)
		l.abort(ReasonSettlementFailed)
		return true
	}

	l.live = false
	l.engine.notifier.RoundSettled(out)
	return out.RoomClosed
}

// abort refunds open stakes and closes the room, then announces it.
func (l *roomLoop) abort(reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), l.engine.timeout)
	defer cancel()

	out, err := l.engine.opts.AbortRoom.Execute(ctx, AbortRoomInput{RoomID: l.roomID, Reason: reason})
	if err != nil {
		if !errors.Is(err, game.ErrRoomClosed) {
			l.engine.logger.Error("engine: failed to abort room", "room_id", l.roomID, "reason", reason, "error", err)
		}
		return
	}
	l.live = false
	l.engine.notifier.RoomClosed(out)
}
