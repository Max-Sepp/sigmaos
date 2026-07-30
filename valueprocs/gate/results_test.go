package gate

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"sigmaos/valueprocs/policy"
)

func refOf(node string, run policy.RunID) policy.RunRef {
	return policy.RunRef{Tree: "t", Node: policy.NodeID(node), Run: run}
}

// finish drives one leaf all the way to a recorded result.
func finish(g *Gate, node string, result []byte) {
	r := refOf(node, 0)
	g.OnRunStarted(r)
	g.OnRunCompleted(r, result)
}

// read is the non-blocking read, which is what most assertions want.
func read(t *testing.T, g *Gate, since uint64) Batch {
	t.Helper()
	b, err := g.Results("t", since, 0)
	assert.NoError(t, err)
	return b
}

func TestResultsAppendInOrder(t *testing.T) {
	g, e, _ := newGate(t)
	defer g.Close()

	_, err := g.Submit(policy.TreeSpec{ID: "t", Root: tree(t, 3, 3)})
	assert.NoError(t, err)
	eventually(t, func() bool { return e.nStarts() == 3 }, "all three started")

	finish(g, "r.1", []byte("b"))
	finish(g, "r.0", []byte("a"))

	b := read(t, g, 0)
	assert.Len(t, b.Results, 2)

	// Sequence numbers follow the order things happened, not the order the
	// tree declared them, which is the only ordering a reader can rely on.
	assert.EqualValues(t, 1, b.Results[0].Seq)
	assert.EqualValues(t, 2, b.Results[1].Seq)
	assert.Equal(t, refOf("r.1", 0), b.Results[0].Ref)
	assert.Equal(t, []byte("b"), b.Results[0].Data)
	assert.Equal(t, []byte("a"), b.Results[1].Data)
	assert.EqualValues(t, 2, b.Next)
}

func TestResultsCarryTheirLabel(t *testing.T) {
	g, e, _ := newGate(t)
	defer g.Close()

	// A label is the only way a reader can tell which of fifteen
	// interchangeable leaves a result came from.
	leaf := policy.WithLabel(leafG(t, "w"), "trial-7")
	_, err := g.Submit(policy.TreeSpec{ID: "t", Root: selG(t, 1, leaf)})
	assert.NoError(t, err)
	eventually(t, func() bool { return e.nStarts() == 1 }, "started")

	finish(g, "r.0", nil)
	assert.Equal(t, "trial-7", read(t, g, 0).Results[0].Label)
}

func TestFinalResultAndDoneArriveTogether(t *testing.T) {
	g, e, _ := newGate(t)
	defer g.Close()

	_, err := g.Submit(policy.TreeSpec{ID: "t", Root: tree(t, 2, 2)})
	assert.NoError(t, err)
	eventually(t, func() bool { return e.nStarts() == 2 }, "both started")

	finish(g, "r.0", []byte("a"))
	b := read(t, g, 0)
	assert.False(t, b.Done, "one of two is not done")

	finish(g, "r.1", []byte("b"))

	// The batch that reports the tree finished must also contain the result
	// that finished it. Were these two answers to two questions, a reader
	// could stop on the first and never collect the second.
	b = read(t, g, b.Next)
	assert.True(t, b.Done)
	assert.Len(t, b.Results, 1)
	assert.Equal(t, []byte("b"), b.Results[0].Data)
	assert.Equal(t, policy.NodeSatisfied, b.State)
}

func TestSameCursorTwiceGivesTheSameAnswer(t *testing.T) {
	g, e, _ := newGate(t)
	defer g.Close()

	_, err := g.Submit(policy.TreeSpec{ID: "t", Root: tree(t, 3, 3)})
	assert.NoError(t, err)
	eventually(t, func() bool { return e.nStarts() == 3 }, "all started")
	finish(g, "r.0", []byte("a"))
	finish(g, "r.1", []byte("b"))

	// Reading consumes nothing, so a call that fails halfway can be retried.
	first := read(t, g, 0)
	for range 5 {
		assert.Equal(t, first, read(t, g, 0))
	}
}

func TestReadersAtDifferentPositionsDoNotInterfere(t *testing.T) {
	g, e, _ := newGate(t)
	defer g.Close()

	_, err := g.Submit(policy.TreeSpec{ID: "t", Root: tree(t, 3, 3)})
	assert.NoError(t, err)
	eventually(t, func() bool { return e.nStarts() == 3 }, "all started")
	finish(g, "r.0", []byte("a"))
	finish(g, "r.1", []byte("b"))
	finish(g, "r.2", []byte("c"))

	// One reader is caught up, another is behind. Neither holds state in the
	// gate, so neither is aware of the other.
	behind := read(t, g, 1)
	assert.Len(t, behind.Results, 2)
	ahead := read(t, g, 3)
	assert.Empty(t, ahead.Results)
	assert.Equal(t, behind, read(t, g, 1))
}

func TestResultsBlockUntilSomethingLands(t *testing.T) {
	g, e, _ := newGate(t)
	defer g.Close()

	_, err := g.Submit(policy.TreeSpec{ID: "t", Root: tree(t, 2, 2)})
	assert.NoError(t, err)
	eventually(t, func() bool { return e.nStarts() == 2 }, "both started")

	got := make(chan Batch, 1)
	go func() {
		b, err := g.Results("t", 0, 2*time.Second)
		assert.NoError(t, err)
		got <- b
	}()

	select {
	case <-got:
		t.Fatal("returned before anything finished")
	case <-time.After(20 * time.Millisecond):
	}

	finish(g, "r.0", []byte("a"))

	select {
	case b := <-got:
		assert.Len(t, b.Results, 1)
		assert.EqualValues(t, 1, b.Next)
	case <-time.After(2 * time.Second):
		t.Fatal("a result landed and the reader was not woken")
	}
}

func TestResultsWakeWhenTheTreeFinishesWithoutResults(t *testing.T) {
	g, e, _ := newGate(t)
	defer g.Close()

	_, err := g.Submit(policy.TreeSpec{ID: "t", Root: tree(t, 1, 1)})
	assert.NoError(t, err)
	eventually(t, func() bool { return e.nStarts() == 1 }, "started")

	got := make(chan Batch, 1)
	go func() {
		b, err := g.Results("t", 0, 2*time.Second)
		assert.NoError(t, err)
		got <- b
	}()

	// The leaf fails permanently, so the tree ends having produced nothing.
	// A reader waiting for a result it will never get must still be released.
	g.OnRunStarted(refOf("r.0", 0))
	g.OnRunFailed(refOf("r.0", 0), policy.FailPermanent, "no")

	select {
	case b := <-got:
		assert.True(t, b.Done)
		assert.Empty(t, b.Results)
		assert.Equal(t, policy.NodeFailed, b.State)
	case <-time.After(2 * time.Second):
		t.Fatal("a finished tree left a reader blocked")
	}
}

func TestResultsGiveUpAfterTheWait(t *testing.T) {
	g, e, _ := newGate(t)
	defer g.Close()

	_, err := g.Submit(policy.TreeSpec{ID: "t", Root: tree(t, 2, 2)})
	assert.NoError(t, err)
	eventually(t, func() bool { return e.nStarts() == 2 }, "both started")

	// A bounded wait, so a reader parked over a transport with its own
	// timeouts comes back empty rather than being cut off mid-call.
	start := time.Now()
	b, err := g.Results("t", 0, 30*time.Millisecond)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 30*time.Millisecond)
	assert.Empty(t, b.Results)
	assert.False(t, b.Done)
	assert.EqualValues(t, 0, b.Next)
}

func TestOnlyCompletionsTheSchedulerAcceptedAreRecorded(t *testing.T) {
	g, e, _ := newGate(t)
	defer g.Close()

	_, err := g.Submit(policy.TreeSpec{ID: "t", Root: tree(t, 1, 1)})
	assert.NoError(t, err)
	eventually(t, func() bool { return e.nStarts() == 1 }, "started")

	// A completion naming an attempt that is not the leaf's current run. The
	// scheduler drops it, and so must the log, or a reader collects a result
	// for work the tree does not consider done.
	g.OnRunCompleted(refOf("r.0", 9), []byte("stale"))
	b := read(t, g, 0)
	assert.Empty(t, b.Results)
	assert.False(t, b.Done)

	finish(g, "r.0", []byte("real"))
	assert.Len(t, read(t, g, 0).Results, 1)
}

func TestStoppedAndFailedLeavesProduceNoEntry(t *testing.T) {
	g, e, _ := newGate(t)
	defer g.Close()

	// Select(1, 3): one leaf succeeds, and the other two are stopped for it.
	_, err := g.Submit(policy.TreeSpec{ID: "t", Root: tree(t, 1, 3)})
	assert.NoError(t, err)
	eventually(t, func() bool { return e.nStarts() == 3 }, "all started")

	for _, n := range []string{"r.0", "r.1", "r.2"} {
		g.OnRunStarted(refOf(n, 0))
	}
	g.OnRunCompleted(refOf("r.0", 0), []byte("won"))
	g.OnRunStopped(refOf("r.1", 0), []byte("half"))
	g.OnRunFailed(refOf("r.2", 0), policy.FailTransient, "crash")

	// Only successes produce results. A reader counting to three would wait
	// forever, which is exactly what Done is for.
	b := read(t, g, 0)
	assert.Len(t, b.Results, 1)
	assert.Equal(t, []byte("won"), b.Results[0].Data)
	assert.True(t, b.Done)
}

func TestCloseWakesAParkedReader(t *testing.T) {
	g, _, _ := newGate(t)

	_, err := g.Submit(policy.TreeSpec{ID: "t", Root: tree(t, 1, 1)})
	assert.NoError(t, err)

	got := make(chan error, 1)
	go func() {
		_, err := g.Results("t", 0, 5*time.Second)
		got <- err
	}()
	time.Sleep(20 * time.Millisecond)
	g.Close()

	select {
	case err := <-got:
		assert.ErrorIs(t, err, ErrClosed)
	case <-time.After(2 * time.Second):
		t.Fatal("Close left a reader blocked forever")
	}
}

func TestResultsUnknownTree(t *testing.T) {
	g, _, _ := newGate(t)
	defer g.Close()
	_, err := g.Results("nope", 0, 0)
	assert.ErrorIs(t, err, ErrUnknownTree)
}

func TestEpochIsReportedWithEveryBatch(t *testing.T) {
	e, c := newExec(), newClock()
	s := policy.NewScheduler(policy.DefaultConfig(), e, nil, nil)
	g := New(s, WithClock(c.now), WithEpoch(42))
	defer g.Close()

	_, err := g.Submit(policy.TreeSpec{ID: "t", Root: tree(t, 1, 1)})
	assert.NoError(t, err)

	// A position is only meaningful against the state that produced it, so
	// every answer says which state that was.
	assert.EqualValues(t, 42, g.Epoch())
	assert.EqualValues(t, 42, read(t, g, 0).Epoch)
}

func TestConcurrentReadersAndWriters(t *testing.T) {
	g, e, _ := newGate(t)
	defer g.Close()

	_, err := g.Submit(policy.TreeSpec{ID: "t", Root: tree(t, 8, 8)})
	assert.NoError(t, err)
	eventually(t, func() bool { return e.nStarts() == 8 }, "all started")

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() { finish(g, "r."+string(rune('0'+i)), []byte{byte(i)}) })
	}
	for range 4 {
		wg.Go(func() {
			cur := uint64(0)
			for range 50 {
				b, err := g.Results("t", cur, 0)
				assert.NoError(t, err)
				// A cursor only ever moves forward.
				assert.GreaterOrEqual(t, b.Next, cur)
				cur = b.Next
			}
		})
	}
	wg.Wait()

	b := read(t, g, 0)
	assert.Len(t, b.Results, 8)
	for i, r := range b.Results {
		assert.EqualValues(t, i+1, r.Seq, "sequence numbers are dense and in order")
	}
}
