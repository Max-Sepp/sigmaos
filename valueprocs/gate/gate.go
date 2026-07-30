// Package gate serializes access to a scheduler and performs the effects its
// decisions produce.
//
// A policy.Scheduler is deliberately not safe for concurrent use, and this is
// where that safety comes from instead: every path into it runs inside step,
// which holds one mutex for the whole call. Nothing else may hold a reference
// to the scheduler.
//
// Effects run after the mutex is released. That is the reason step exists at
// all rather than callers taking the lock themselves: starting and stopping
// work reaches a platform that may block for seconds, and holding the
// scheduler's lock across that would stall every score report, submission and
// tick behind it.
//
// The package is general to cluster systems and imports only the standard
// library and policy. Transport, wire formats and the platform itself all sit
// outside it.
package gate

import (
	"errors"
	"sync"
	"time"

	"sigmaos/valueprocs/policy"
)

var (
	ErrClosed      = errors.New("gate: closed")
	ErrUnknownTree = errors.New("gate: unknown tree")

	// ErrStaleEpoch means a reader's position belongs to an incarnation of
	// this state that no longer exists. Everything it knows about the tree is
	// void, and the only recovery is to start over.
	ErrStaleEpoch = errors.New("gate: stale epoch")
)

// DefaultTick is how often the scheduler is given the current time. It is the
// only clock in the system; policy never reads one.
const DefaultTick = 250 * time.Millisecond

// Gate owns a scheduler and is the only thing permitted to touch it.
type Gate struct {
	mu     sync.Mutex
	cond   *sync.Cond
	sched  *policy.Scheduler
	closed bool

	now  func() time.Time
	tick time.Duration

	// logs is the append-only record of what each tree produced, and epoch
	// identifies this incarnation of it. Both live here rather than a layer
	// out because an entry has to be appended inside the same step that told
	// the scheduler: were they two critical sections, a reader could observe
	// a tree finished before it had been handed the tree's last result.
	logs  map[policy.TreeID][]LeafResult
	epoch uint64

	done chan struct{}
	wg   sync.WaitGroup
}

// Opt configures a Gate.
type Opt func(*Gate)

// WithClock replaces the source of time, which is what lets a test drive the
// tick by hand.
func WithClock(f func() time.Time) Opt {
	return func(g *Gate) { g.now = f }
}

// WithTick sets how often Tick is called once Run has started.
func WithTick(d time.Duration) Opt {
	return func(g *Gate) { g.tick = d }
}

// WithEpoch identifies this incarnation of the gate's state.
//
// The gate never invents the value, because a number that must differ across
// restarts has to come from something that outlives one — a process
// identifier, a start time, a stored counter. Which of those is available is
// a property of the platform, so the platform supplies it.
func WithEpoch(e uint64) Opt {
	return func(g *Gate) { g.epoch = e }
}

// New returns a Gate wrapping sched. The caller must not retain sched.
func New(sched *policy.Scheduler, opts ...Opt) *Gate {
	g := &Gate{
		sched: sched,
		now:   time.Now,
		tick:  DefaultTick,
		logs:  make(map[policy.TreeID][]LeafResult),
		done:  make(chan struct{}),
	}
	g.cond = sync.NewCond(&g.mu)
	for _, o := range opts {
		o(g)
	}
	return g
}

// step is the funnel. Every entry point goes through it, so the scheduler
// only ever runs one at a time.
func (g *Gate) step(f func(*policy.Scheduler, time.Time) policy.Effect) {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	eff := f(g.sched, g.now())

	// Waiters re-read scheduler state, so they are woken while the lock is
	// still held and cannot observe a decision that has not been made yet.
	g.cond.Broadcast()
	g.mu.Unlock()

	if eff == nil {
		return
	}
	// One goroutine for the whole effect rather than one per call inside it:
	// the calls run in the order they were decided, which is what lets a stop
	// reach the platform before the start waiting on its slot.
	g.wg.Go(eff)
}

// --- demand ----------------------------------------------------------------

// Submit registers a tree and starts whatever its quorum requires.
func (g *Gate) Submit(spec policy.TreeSpec) (policy.TreeID, error) {
	var (
		id  policy.TreeID
		err error
	)
	ran := false
	g.step(func(s *policy.Scheduler, now time.Time) policy.Effect {
		ran = true
		var eff policy.Effect
		id, eff, err = s.SubmitTree(now, spec)
		return eff
	})
	if !ran {
		return "", ErrClosed
	}
	return id, err
}

// Cancel stops everything a tree is running. It is idempotent.
func (g *Gate) Cancel(id policy.TreeID) error {
	var err error
	ran := false
	g.step(func(s *policy.Scheduler, now time.Time) policy.Effect {
		ran = true
		var eff policy.Effect
		eff, err = s.CancelTree(now, id)
		return eff
	})
	if !ran {
		return ErrClosed
	}
	return err
}

// --- value -----------------------------------------------------------------

// OnScore records what a running attempt reports about itself.
func (g *Gate) OnScore(ref policy.RunRef, sc policy.Score, gr policy.Gradient) {
	g.step(func(s *policy.Scheduler, now time.Time) policy.Effect {
		return s.OnScore(now, ref, sc, gr)
	})
}

// --- lifecycle -------------------------------------------------------------

// OnRunStarted reports that an attempt has been placed and is running.
func (g *Gate) OnRunStarted(ref policy.RunRef) {
	g.step(func(s *policy.Scheduler, now time.Time) policy.Effect {
		return s.OnRunStarted(now, ref)
	})
}

// OnRunCompleted reports that an attempt finished its work.
func (g *Gate) OnRunCompleted(ref policy.RunRef, result []byte) {
	g.step(func(s *policy.Scheduler, now time.Time) policy.Effect {
		eff := s.OnRunCompleted(now, ref, result)
		// Only what the scheduler accepted is recorded. An event naming a
		// superseded attempt is dropped there, and logging it anyway would
		// hand a reader a result for work the tree does not consider done.
		if s.Succeeded(ref) {
			g.appendResultL(s, ref, result)
		}
		return eff
	})
}

// OnRunStopped reports that an attempt ended because it was asked to.
func (g *Gate) OnRunStopped(ref policy.RunRef, partial []byte) {
	g.step(func(s *policy.Scheduler, now time.Time) policy.Effect {
		return s.OnRunStopped(now, ref, partial)
	})
}

// OnRunFailed reports that an attempt ended badly.
func (g *Gate) OnRunFailed(ref policy.RunRef, k policy.FailureKind, msg string) {
	g.step(func(s *policy.Scheduler, now time.Time) policy.Effect {
		return s.OnRunFailed(now, ref, k, msg)
	})
}

// --- contention ------------------------------------------------------------

// OnOccupancy records how contended the platform reports itself to be.
func (g *Gate) OnOccupancy(o policy.Occupancy) {
	g.step(func(s *policy.Scheduler, now time.Time) policy.Effect {
		return s.OnOccupancy(now, o)
	})
}

// Tick advances the scheduler's clock once. Run does this on a timer; a test
// calls it directly.
func (g *Gate) Tick() {
	g.step(func(s *policy.Scheduler, now time.Time) policy.Effect {
		return s.Tick(now)
	})
}

// --- read models -----------------------------------------------------------

// Status reports a tree's current state.
func (g *Gate) Status(id policy.TreeID) (policy.TreeView, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sched.TreeView(id)
}

// Stats reports the scheduler's counters.
func (g *Gate) Stats() policy.Stats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sched.Stats()
}

// Wait blocks until a tree is no longer pending, and returns its final state.
// It returns ErrClosed if the gate shuts down first.
func (g *Gate) Wait(id policy.TreeID) (policy.TreeView, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for {
		v, ok := g.sched.TreeView(id)
		if !ok {
			return policy.TreeView{}, ErrUnknownTree
		}
		if v.State != policy.NodePending || v.Cancelled {
			return v, nil
		}
		if g.closed {
			return v, ErrClosed
		}
		g.cond.Wait()
	}
}

// --- lifecycle -------------------------------------------------------------

// Run starts the ticker. It returns immediately.
func (g *Gate) Run() {
	g.wg.Go(func() {
		t := time.NewTicker(g.tick)
		defer t.Stop()
		for {
			select {
			case <-g.done:
				return
			case <-t.C:
				g.Tick()
			}
		}
	})
}

// Close stops accepting work, wakes every waiter and drains outstanding
// effects. Cancelling trees first is the caller's business, since only it
// knows whether the work should outlive the process.
func (g *Gate) Close() {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.closed = true
	close(g.done)
	g.cond.Broadcast()
	g.mu.Unlock()

	g.wg.Wait()
}

// CancelAll stops every tree, for a shutdown that should not leave work
// running behind it.
func (g *Gate) CancelAll() {
	g.mu.Lock()
	views := g.sched.Trees()
	ids := make([]policy.TreeID, 0, len(views))
	for _, v := range views {
		ids = append(ids, v.ID)
	}
	g.mu.Unlock()
	for _, id := range ids {
		g.Cancel(id)
	}
}
