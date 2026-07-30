package gate

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"sigmaos/valueprocs/policy"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

type work string

func (w work) Name() string { return string(w) }

// exec records what reached the platform. It takes its own lock, because
// effects run on goroutines by design.
type exec struct {
	mu     sync.Mutex
	starts []policy.RunRef
	stops  []policy.RunRef

	// hold blocks every call until released, which is how a test proves the
	// scheduler's lock is not held while the platform is slow.
	hold chan struct{}
	// inflight is how many calls are blocked in the platform right now.
	inflight atomic.Int32
}

func newExec() *exec { return &exec{} }

func (e *exec) block() { e.hold = make(chan struct{}) }

func (e *exec) release() { close(e.hold) }

func (e *exec) wait() {
	if e.hold != nil {
		e.inflight.Add(1)
		<-e.hold
		e.inflight.Add(-1)
	}
}

func (e *exec) Start(ref policy.RunRef, l policy.Launch, why policy.StartReason) {
	e.wait()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.starts = append(e.starts, ref)
}

func (e *exec) Stop(ref policy.RunRef, why policy.StopReason) {
	e.wait()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stops = append(e.stops, ref)
}

func (e *exec) nStarts() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.starts)
}

func (e *exec) nStops() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.stops)
}

// clock is a hand-advanced time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: t0} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newGate(t *testing.T) (*Gate, *exec, *clock) {
	t.Helper()
	e, c := newExec(), newClock()
	s := policy.NewScheduler(policy.DefaultConfig(), e, nil, nil)
	return New(s, WithClock(c.now)), e, c
}

func leafG(t *testing.T, name string) policy.Group {
	t.Helper()
	g, err := policy.Leaf(work(name))
	assert.NoError(t, err)
	return g
}

func selG(t *testing.T, k int, cs ...policy.Group) policy.Group {
	t.Helper()
	g, err := policy.Select(k, cs...)
	assert.NoError(t, err)
	return g
}

func tree(t *testing.T, k, n int) policy.Group {
	t.Helper()
	cs := make([]policy.Group, n)
	for i := range cs {
		cs[i] = leafG(t, "w")
	}
	return selG(t, k, cs...)
}

// eventually polls because effects run on their own goroutines, so a decision
// and the platform call it produces are not simultaneous.
func eventually(t *testing.T, f func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting: %s", msg)
}

func TestSubmitRunsEffect(t *testing.T) {
	g, e, _ := newGate(t)
	defer g.Close()

	id, err := g.Submit(policy.TreeSpec{ID: "t", Root: tree(t, 2, 3)})
	assert.NoError(t, err)
	assert.Equal(t, policy.TreeID("t"), id)

	eventually(t, func() bool { return e.nStarts() == 3 }, "three starts reach the platform")
}

func TestSubmitErrorsSurface(t *testing.T) {
	g, _, _ := newGate(t)
	defer g.Close()

	_, err := g.Submit(policy.TreeSpec{Root: tree(t, 1, 1)})
	assert.ErrorIs(t, err, policy.ErrNoTreeID)

	_, err = g.Submit(policy.TreeSpec{ID: "t", Root: tree(t, 1, 1)})
	assert.NoError(t, err)
	_, err = g.Submit(policy.TreeSpec{ID: "t", Root: tree(t, 1, 1)})
	assert.ErrorIs(t, err, policy.ErrDuplicateTree)
}

func TestEffectsRunWithoutTheSchedulerLock(t *testing.T) {
	g, e, _ := newGate(t)
	defer func() { e.release(); g.Close() }()

	// Every platform call blocks. If effects ran under the scheduler's mutex,
	// nothing below could make progress.
	e.block()
	_, err := g.Submit(policy.TreeSpec{ID: "t", Root: tree(t, 3, 3)})
	assert.NoError(t, err)
	eventually(t, func() bool { return e.inflight.Load() > 0 }, "a call is stuck in the platform")

	// Reads and further steps must still be served while it is stuck.
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.Stats()
		g.Tick()
		g.OnOccupancy(policy.Occupancy{Busy: 0.5})
		_, _ = g.Status("t")
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("gate wedged behind a blocked platform call")
	}
}

func TestStepSerializesConcurrentCallers(t *testing.T) {
	g, _, _ := newGate(t)
	defer g.Close()

	_, err := g.Submit(policy.TreeSpec{ID: "t", Root: tree(t, 4, 4)})
	assert.NoError(t, err)

	// The scheduler is not safe for concurrent use, so this is only sound if
	// step is doing its job. Run under -race to mean anything.
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			ref := policy.RunRef{Tree: "t", Node: policy.NodeID("r." + string(rune('0'+i%4))), Run: 0}
			for j := range 50 {
				g.OnScore(ref, policy.Score(j), 0)
				g.Tick()
				g.Stats()
				_, _ = g.Status("t")
			}
		})
	}
	wg.Wait()
}

func TestWaitReturnsWhenTreeSettles(t *testing.T) {
	g, e, _ := newGate(t)
	defer g.Close()

	_, err := g.Submit(policy.TreeSpec{ID: "t", Root: tree(t, 1, 2)})
	assert.NoError(t, err)
	eventually(t, func() bool { return e.nStarts() > 0 }, "work started")

	got := make(chan policy.TreeView, 1)
	go func() {
		v, err := g.Wait("t")
		assert.NoError(t, err)
		got <- v
	}()

	// The waiter must still be blocked: nothing has finished.
	select {
	case <-got:
		t.Fatal("Wait returned while the tree was still pending")
	case <-time.After(20 * time.Millisecond):
	}

	g.OnRunStarted(policy.RunRef{Tree: "t", Node: "r.0"})
	g.OnRunCompleted(policy.RunRef{Tree: "t", Node: "r.0"}, nil)

	select {
	case v := <-got:
		assert.Equal(t, policy.NodeSatisfied, v.State)
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not wake when the tree settled")
	}
}

func TestWaitUnknownTree(t *testing.T) {
	g, _, _ := newGate(t)
	defer g.Close()
	_, err := g.Wait("nope")
	assert.ErrorIs(t, err, ErrUnknownTree)
}

func TestWaitWakesOnClose(t *testing.T) {
	g, _, _ := newGate(t)
	_, err := g.Submit(policy.TreeSpec{ID: "t", Root: tree(t, 1, 1)})
	assert.NoError(t, err)

	got := make(chan error, 1)
	go func() {
		_, err := g.Wait("t")
		got <- err
	}()
	time.Sleep(20 * time.Millisecond)
	g.Close()

	select {
	case err := <-got:
		assert.ErrorIs(t, err, ErrClosed)
	case <-time.After(2 * time.Second):
		t.Fatal("Close left a waiter blocked forever")
	}
}

func TestCancelStopsWork(t *testing.T) {
	g, e, _ := newGate(t)
	defer g.Close()

	_, err := g.Submit(policy.TreeSpec{ID: "t", Root: tree(t, 2, 2)})
	assert.NoError(t, err)
	eventually(t, func() bool { return e.nStarts() == 2 }, "work started")

	assert.NoError(t, g.Cancel("t"))
	eventually(t, func() bool { return e.nStops() == 2 }, "both attempts stopped")

	assert.ErrorIs(t, g.Cancel("nope"), policy.ErrUnknownTree)
}

func TestCancelAllStopsEveryTree(t *testing.T) {
	g, e, _ := newGate(t)
	defer g.Close()

	for _, id := range []policy.TreeID{"a", "b", "c"} {
		_, err := g.Submit(policy.TreeSpec{ID: id, Root: tree(t, 1, 1)})
		assert.NoError(t, err)
	}
	eventually(t, func() bool { return e.nStarts() == 3 }, "all three started")

	g.CancelAll()
	eventually(t, func() bool { return e.nStops() == 3 }, "all three stopped")
}

func TestTickerAdvancesTheClock(t *testing.T) {
	e, c := newExec(), newClock()
	cfg := policy.DefaultConfig()
	s := policy.NewScheduler(cfg, e, nil, nil)
	g := New(s, WithClock(c.now), WithTick(time.Millisecond))
	defer g.Close()

	_, err := g.Submit(policy.TreeSpec{ID: "t", Root: tree(t, 2, 2)})
	assert.NoError(t, err)
	assert.Zero(t, g.Stats().Pressure)

	// Attempts are started but never observed running, so letting the clock
	// run forward is enough for queueing delay alone to register as pressure.
	g.Run()
	c.advance(4 * cfg.QueueDelayTarget)

	eventually(t, func() bool { return g.Stats().DelayPressure > 0 },
		"the ticker fed the scheduler a later time")
}

func TestClosedGateAcceptsNothing(t *testing.T) {
	g, e, _ := newGate(t)
	g.Close()

	_, err := g.Submit(policy.TreeSpec{ID: "t", Root: tree(t, 1, 1)})
	assert.ErrorIs(t, err, ErrClosed)
	assert.ErrorIs(t, g.Cancel("t"), ErrClosed)

	// The read models still answer; they are what a shutdown path inspects.
	assert.NotPanics(t, func() { g.Stats() })
	assert.Equal(t, 0, e.nStarts())

	// Close is idempotent.
	assert.NotPanics(t, func() { g.Close() })
}

func TestCloseDrainsOutstandingEffects(t *testing.T) {
	g, e, _ := newGate(t)

	e.block()
	_, err := g.Submit(policy.TreeSpec{ID: "t", Root: tree(t, 3, 3)})
	assert.NoError(t, err)
	eventually(t, func() bool { return e.inflight.Load() > 0 }, "a call is in the platform")

	closed := make(chan struct{})
	go func() { g.Close(); close(closed) }()

	select {
	case <-closed:
		t.Fatal("Close returned while an effect was still running")
	case <-time.After(50 * time.Millisecond):
	}

	e.release()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not drain")
	}
}
