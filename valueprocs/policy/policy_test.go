package policy

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// --- construction ----------------------------------------------------------

func TestSelectRejectsBadShape(t *testing.T) {
	l := leafG(t, "a")
	for _, tc := range []struct {
		name string
		k    int
		cs   []Group
		err  error
	}{
		{"no children", 1, nil, ErrNoChildren},
		{"k below one", 0, []Group{l}, ErrBadK},
		{"k above n", 2, []Group{l}, ErrBadK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Select(tc.k, tc.cs...)
			assert.ErrorIs(t, err, tc.err)
		})
	}
	_, err := Leaf(nil)
	assert.ErrorIs(t, err, ErrNoWorkload)
}

func TestSubmitRejectsDuplicateAndEmpty(t *testing.T) {
	s, _ := newSched(testConfig())
	root := selG(t, 1, leafG(t, "a"))

	_, _, err := s.SubmitTree(t0, TreeSpec{Root: root})
	assert.ErrorIs(t, err, ErrNoTreeID)
	_, _, err = s.SubmitTree(t0, TreeSpec{ID: "t"})
	assert.ErrorIs(t, err, ErrNoRoot)

	_, _, err = s.SubmitTree(t0, TreeSpec{ID: "t", Root: root})
	assert.NoError(t, err)
	_, _, err = s.SubmitTree(t0, TreeSpec{ID: "t", Root: root})
	assert.ErrorIs(t, err, ErrDuplicateTree)
}

func TestNodeIDsFollowTreePath(t *testing.T) {
	s, _ := newSched(testConfig())
	inner := selG(t, 1, leafG(t, "c"), leafG(t, "d"))
	submit(t, s, t0, "t", selG(t, 2, leafG(t, "a"), inner))

	v, ok := s.TreeView("t")
	assert.True(t, ok)
	var ids []NodeID
	for _, n := range v.Nodes {
		ids = append(ids, n.ID)
	}
	assert.Equal(t, []NodeID{"r", "r.0", "r.1", "r.1.0", "r.1.1"}, ids)
}

// --- tree settling ---------------------------------------------------------

func TestSelectSatisfiedAtK(t *testing.T) {
	s, f := newSched(testConfig())
	submit(t, s, t0, "t", selG(t, 2, leavesG(t, 3)...))
	startQueued(s, f, t0)
	f.reset()

	apply(s.OnRunCompleted(t0, refOf("t", "r.0", 0), nil))
	assert.Equal(t, NodePending, nodeView(t, s, "t", "r").State)
	apply(s.OnRunCompleted(t0, refOf("t", "r.1", 0), nil))

	assert.Equal(t, NodeSatisfied, nodeView(t, s, "t", "r").State)
	if assert.Len(t, f.stops(), 1) {
		assert.Equal(t, NodeID("r.2"), f.stops()[0].ref.Node)
		assert.Equal(t, StopQuorumReached, f.stops()[0].stop.Kind)
	}
}

func TestSelectFailsWhenAliveBelowK(t *testing.T) {
	s, f := newSched(testConfig())
	submit(t, s, t0, "t", selG(t, 2, leavesG(t, 3)...))
	startQueued(s, f, t0)

	apply(s.OnRunFailed(t0, refOf("t", "r.0", 0), FailPermanent, "boom"))
	assert.Equal(t, NodePending, nodeView(t, s, "t", "r").State)
	apply(s.OnRunFailed(t0, refOf("t", "r.1", 0), FailPermanent, "boom"))

	assert.Equal(t, NodeFailed, nodeView(t, s, "t", "r").State)
}

func TestFailureCascadesToRootInOnePass(t *testing.T) {
	s, f := newSched(testConfig())
	inner := selG(t, 1, leafG(t, "c"))
	submit(t, s, t0, "t", selG(t, 2, leafG(t, "a"), inner))
	startQueued(s, f, t0)

	// The only leaf under the inner node dies, so the inner node fails, so the
	// root can no longer reach k. All three states must settle together.
	apply(s.OnRunFailed(t0, refOf("t", "r.1.0", 0), FailPermanent, "boom"))

	assert.Equal(t, NodeFailed, nodeView(t, s, "t", "r.1.0").State)
	assert.Equal(t, NodeFailed, nodeView(t, s, "t", "r.1").State)
	assert.Equal(t, NodeFailed, nodeView(t, s, "t", "r").State)
}

// --- the three motivating shapes -------------------------------------------

func TestPrunesToKUnderPressure(t *testing.T) {
	s, f := newSched(testConfig())
	submit(t, s, t0, "t", selG(t, 1, leavesG(t, 15)...))
	assert.Len(t, f.starts(), 15, "an idle cluster runs every candidate")
	startQueued(s, f, t0)

	// Highest index scores best, so the survivor must not be the first child.
	for i := 0; i < 15; i++ {
		apply(s.OnScore(t0, refOf("t", NodeID(fmt.Sprintf("r.%d", i)), 0), Score(i), 0))
	}
	f.reset()

	busy(s, t0, 1.0)
	assert.Len(t, f.stops(), 14)
	assert.Equal(t, 1, nodeView(t, s, "t", "r").Target)

	stopped := map[NodeID]bool{}
	for _, c := range f.stops() {
		stopped[c.ref.Node] = true
		assert.Equal(t, StopOutrankedBySibling, c.stop.Kind)
	}
	assert.False(t, stopped["r.14"], "the best-scoring child must survive")
}

func TestQuorumSurplusShedByScore(t *testing.T) {
	s, f := newSched(testConfig())
	submit(t, s, t0, "t", selG(t, 6, leavesG(t, 9)...))
	startQueued(s, f, t0)
	for i := 0; i < 9; i++ {
		apply(s.OnScore(t0, refOf("t", NodeID(fmt.Sprintf("r.%d", i)), 0), Score(i), 0))
	}
	f.reset()

	busy(s, t0, 1.0)
	assert.Len(t, f.stops(), 3)
	for _, c := range f.stops() {
		assert.Contains(t, []NodeID{"r.0", "r.1", "r.2"}, c.ref.Node,
			"the three lowest-scoring children lose")
	}
}

func TestSpeculativeRacerOnlyWhenSlackAndGradient(t *testing.T) {
	// A pressure that leaves the contention term at k but stays under
	// ExpandAt, so the racer decision is the only thing that can move target.
	const p = 0.52

	newRace := func(t *testing.T) (*Scheduler, *fake) {
		t.Helper()
		s, f := newSched(testConfig())
		// Pressure first: the deadband holds a target once set, so a tree
		// submitted onto an idle cluster would start at n and stay there.
		busy(s, t0, p)
		submit(t, s, t0, "t", selG(t, 1, leafG(t, "a"), leafG(t, "b")))
		startQueued(s, f, t0)
		return s, f
	}

	t.Run("low gradient does not race", func(t *testing.T) {
		s, f := newRace(t)
		f.reset()
		apply(s.OnScore(t0, refOf("t", "r.0", 0), 0.5, 0.05))
		assert.Empty(t, f.starts())
		assert.Equal(t, 1, nodeView(t, s, "t", "r").Target)
	})

	t.Run("no slack does not race", func(t *testing.T) {
		s, f := newRace(t)
		busy(s, t0, 0.9)
		f.reset()
		apply(s.OnScore(t0, refOf("t", "r.0", 0), 0.5, 0.9))
		assert.Empty(t, f.starts())
	})

	t.Run("both hold", func(t *testing.T) {
		s, f := newRace(t)
		f.reset()
		apply(s.OnScore(t0, refOf("t", "r.0", 0), 0.5, 0.9))
		if assert.Len(t, f.starts(), 1) {
			assert.Equal(t, NodeID("r.1"), f.starts()[0].ref.Node)
			assert.Equal(t, StartSpeculativeRacer, f.starts()[0].why.Kind)
		}

		// The winner finishing is what settles the race, and the loser must be
		// stopped as a racer rather than as ordinary quorum surplus.
		startQueued(s, f, t0)
		f.reset()
		apply(s.OnRunCompleted(t0, refOf("t", "r.0", 0), nil))
		if assert.Len(t, f.stops(), 1) {
			assert.Equal(t, NodeID("r.1"), f.stops()[0].ref.Node)
			assert.Equal(t, StopRacerLost, f.stops()[0].stop.Kind)
		}
	})
}

func TestQueuedChildIsNotAStraggler(t *testing.T) {
	cfg := testConfig()
	// Take queueing delay out of the pressure fold, so that the only thing
	// left to explain a racer is whether the child is running.
	cfg.QueueDelayTarget = time.Hour
	s, f := newSched(cfg)
	busy(s, t0, 0.52)
	submit(t, s, t0, "t", selG(t, 1, leafG(t, "a"), leafG(t, "b")))
	f.reset()

	// The first child is started but never observed running. However long it
	// sits there it is waiting rather than slow, so racing it would deepen the
	// queue without addressing anything.
	late := t0.Add(time.Minute)
	apply(s.Tick(late))
	assert.Empty(t, f.starts())
	assert.Equal(t, 1, nodeView(t, s, "t", "r").Target)

	// The same dwell, the same gradient, but now actually running: this time a
	// racer is warranted.
	apply(s.OnRunStarted(late, refOf("t", "r.0", 0)))
	apply(s.OnScore(late, refOf("t", "r.0", 0), 0.5, 0.9))
	if assert.Len(t, f.starts(), 1) {
		assert.Equal(t, StartSpeculativeRacer, f.starts()[0].why.Kind)
	}
}

// --- policy invariants -----------------------------------------------------

func TestScoreNeverExtrapolated(t *testing.T) {
	// Any order-preserving transform of every score must produce exactly the
	// same decisions, because only the ordering carries information.
	trace := func(f func(i int) Score) []string {
		s, fk := newSched(testConfig())
		submit(t, s, t0, "t", selG(t, 2, leavesG(t, 6)...))
		startQueued(s, fk, t0)
		for i := 0; i < 6; i++ {
			apply(s.OnScore(t0, refOf("t", NodeID(fmt.Sprintf("r.%d", i)), 0), f(i), 0))
		}
		busy(s, t0, 0.8)
		busy(s, t0, 0.2)
		return fk.trace()
	}

	fractions := trace(func(i int) Score { return Score(i) / 6 })
	shifted := trace(func(i int) Score { return Score(i)*1000 - 5000 })
	assert.Equal(t, fractions, shifted)
}

func TestNoOscillation(t *testing.T) {
	cfg := testConfig()
	cfg.MinDwell = 5 * time.Second
	s, f := newSched(cfg)
	submit(t, s, t0, "t", selG(t, 1, leavesG(t, 8)...))
	startQueued(s, f, t0)

	// Sweep pressure back and forth across the deadband once a second. Without
	// the dwell every crossing would retarget the node.
	last, changes := nodeView(t, s, "t", "r").Target, 0
	for i := 1; i <= 30; i++ {
		now := t0.Add(time.Duration(i) * time.Second)
		p := 0.2
		if i%2 == 0 {
			p = 0.9
		}
		busy(s, now, p)
		if cur := nodeView(t, s, "t", "r").Target; cur != last {
			changes++
			last = cur
		}
	}
	assert.LessOrEqual(t, changes, 30*int(time.Second)/int(cfg.MinDwell)+1,
		"a node may not reverse its target more than once per dwell")
}

func TestStalenessDoesNotStop(t *testing.T) {
	cfg := testConfig()
	s, f := newSched(cfg)
	submit(t, s, t0, "t", selG(t, 2, leavesG(t, 2)...))
	startQueued(s, f, t0)
	apply(s.OnScore(t0, refOf("t", "r.0", 0), 0.5, 0))
	f.reset()

	apply(s.Tick(t0.Add(10 * cfg.ScoreStale)))

	assert.Empty(t, f.stops(), "silence is not evidence of trouble")
	assert.True(t, nodeView(t, s, "t", "r.0").ScoreStale)
	assert.Equal(t, RRunning, nodeView(t, s, "t", "r.0").RunState)
}

func TestStoppingStaysChargedUntilTerminal(t *testing.T) {
	s, f := newSched(testConfig())
	submit(t, s, t0, "t", selG(t, 1, leavesG(t, 2)...))
	startQueued(s, f, t0)
	charged := s.Stats().NCharged
	f.reset()

	busy(s, t0, 1.0)
	assert.Len(t, f.stops(), 1)
	assert.Equal(t, charged, s.Stats().NCharged,
		"issuing a stop frees nothing; only a terminal event does")

	apply(s.OnRunStopped(t0, f.stops()[0].ref, nil))
	assert.Equal(t, charged-1, s.Stats().NCharged)
}

func TestStoppedSlotNotDoubleClaimed(t *testing.T) {
	s, f := newSched(testConfig())
	apply(s.OnOccupancy(t0, Occupancy{Slots: 1}))

	submit(t, s, t0, "a", selG(t, 1, leafG(t, "a0")))
	startQueued(s, f, t0)
	assert.Len(t, f.starts(), 1)

	// The one slot is taken, so a second tree must wait.
	submit(t, s, t0, "b", selG(t, 1, leafG(t, "b0")))
	f.reset()
	assert.Empty(t, f.starts())

	// Cancelling the first tree issues a stop but does not free the slot.
	eff, err := s.CancelTree(t0, "a")
	assert.NoError(t, err)
	apply(eff)
	assert.Empty(t, f.starts(), "a slot promised to a stop is not free yet")

	apply(s.OnRunStopped(t0, refOf("a", "r.0", 0), nil))
	assert.Len(t, f.starts(), 1, "exactly one claimant, and only once it is free")
	assert.Equal(t, TreeID("b"), f.starts()[0].ref.Tree)
}

func TestQueueDelayRaisesPressure(t *testing.T) {
	cfg := testConfig()
	s, _ := newSched(cfg)
	// Occupancy is constant and idle throughout; only the wait moves.
	apply(s.OnOccupancy(t0, Occupancy{Busy: 0}))
	submit(t, s, t0, "t", selG(t, 2, leavesG(t, 2)...))
	assert.Zero(t, s.Stats().Pressure)

	apply(s.Tick(t0.Add(2 * cfg.QueueDelayTarget)))

	assert.Equal(t, float64(1), s.Stats().DelayPressure)
	assert.Equal(t, float64(1), s.Stats().Pressure,
		"work that cannot be placed is contention, whatever the platform says")
}

func TestRequeuedChildKeepsScore(t *testing.T) {
	s, f := newSched(testConfig())
	submit(t, s, t0, "t", selG(t, 1, leafG(t, "a"), leafG(t, "b")))
	startQueued(s, f, t0)
	apply(s.OnScore(t0, refOf("t", "r.0", 0), 0.9, 0))
	apply(s.OnScore(t0, refOf("t", "r.1", 0), 0.1, 0))
	f.reset()

	busy(s, t0, 1.0)
	if assert.Len(t, f.stops(), 1) {
		assert.Equal(t, NodeID("r.1"), f.stops()[0].ref.Node)
	}
	apply(s.OnRunStopped(t0, refOf("t", "r.1", 0), nil))

	v := nodeView(t, s, "t", "r.1")
	assert.Equal(t, RIdle, v.RunState)
	assert.True(t, v.HasScore)
	assert.Equal(t, Score(0.1), v.Score)
	assert.Equal(t, 0, v.Attempts, "being stopped is not a failure")
	assert.Equal(t, 1, v.Stops)
}

func TestResurrectionWhenLeadersFail(t *testing.T) {
	s, f := newSched(testConfig())
	submit(t, s, t0, "t", selG(t, 1, leafG(t, "a"), leafG(t, "b")))
	startQueued(s, f, t0)
	apply(s.OnScore(t0, refOf("t", "r.0", 0), 0.9, 0))
	apply(s.OnScore(t0, refOf("t", "r.1", 0), 0.1, 0))
	busy(s, t0, 1.0)
	apply(s.OnRunStopped(t0, refOf("t", "r.1", 0), nil))
	f.reset()

	// The loser never re-ranks high enough on its own, but the leader dying
	// leaves it as the only candidate.
	apply(s.OnRunFailed(t0, refOf("t", "r.0", 0), FailPermanent, "boom"))

	if assert.Len(t, f.starts(), 1) {
		assert.Equal(t, NodeID("r.1"), f.starts()[0].ref.Node)
		assert.Equal(t, RunID(1), f.starts()[0].ref.Run, "a fresh attempt")
	}
}

func TestStopNeverExhaustsAttempts(t *testing.T) {
	cfg := testConfig()
	s, f := newSched(cfg)
	submit(t, s, t0, "t", selG(t, 1, leafG(t, "a"), leafG(t, "b")))
	startQueued(s, f, t0)
	apply(s.OnScore(t0, refOf("t", "r.0", 0), 0.9, 0))
	apply(s.OnScore(t0, refOf("t", "r.1", 0), 0.1, 0))

	// Stopping a leaf far more often than MaxAttempts must leave it a
	// candidate: a stop is a decision about the cluster, not evidence that the
	// work is broken.
	for i := 0; i < 3*cfg.MaxAttempts; i++ {
		busy(s, t0, 1.0)
		apply(s.OnRunStopped(t0, refOf("t", "r.1", RunID(i)), nil))
		busy(s, t0, 0.0)
		startQueued(s, f, t0)
		f.reset()
	}

	v := nodeView(t, s, "t", "r.1")
	assert.NotEqual(t, RFailedPerm, v.RunState)
	assert.Equal(t, 0, v.Attempts)
	assert.Equal(t, 3*cfg.MaxAttempts, v.Stops)
}

func TestFailuresExhaustAttempts(t *testing.T) {
	cfg := testConfig()
	s, f := newSched(cfg)
	submit(t, s, t0, "t", selG(t, 1, leafG(t, "a")))
	for i := 0; i < cfg.MaxAttempts; i++ {
		startQueued(s, f, t0)
		apply(s.OnRunFailed(t0, refOf("t", "r.0", RunID(i)), FailTransient, "flaky"))
	}
	assert.Equal(t, RFailedPerm, nodeView(t, s, "t", "r.0").RunState)
	assert.Equal(t, NodeFailed, nodeView(t, s, "t", "r").State)
}

func TestDeterministicReconcile(t *testing.T) {
	replay := func() []string {
		s, f := newSched(testConfig())
		submit(t, s, t0, "t", selG(t, 3, leavesG(t, 9)...))
		startQueued(s, f, t0)
		for i := 8; i >= 0; i-- {
			apply(s.OnScore(t0, refOf("t", NodeID(fmt.Sprintf("r.%d", i)), 0), Score(i%4), 0))
		}
		busy(s, t0, 0.9)
		busy(s, t0, 0.1)
		return f.trace()
	}
	assert.Equal(t, replay(), replay())
}

// --- cancellation and late events ------------------------------------------

func TestCancelStopsEverythingCharged(t *testing.T) {
	s, f := newSched(testConfig())
	submit(t, s, t0, "t", selG(t, 2, leavesG(t, 4)...))
	startQueued(s, f, t0)
	f.reset()

	eff, err := s.CancelTree(t0, "t")
	assert.NoError(t, err)
	apply(eff)

	assert.Len(t, f.stops(), 4)
	for _, c := range f.stops() {
		assert.Equal(t, StopTreeCancelled, c.stop.Kind)
	}

	// A second cancel is a no-op rather than a second round of stops.
	eff, err = s.CancelTree(t0, "t")
	assert.NoError(t, err)
	assert.Nil(t, eff)
}

func TestLateEventForSupersededAttemptIsDropped(t *testing.T) {
	s, f := newSched(testConfig())
	submit(t, s, t0, "t", selG(t, 1, leafG(t, "a"), leafG(t, "b")))
	startQueued(s, f, t0)
	busy(s, t0, 1.0)
	apply(s.OnRunStopped(t0, refOf("t", "r.1", 0), nil))
	f.reset()

	// Run 0 of r.1 is over; anything still referring to it must be ignored.
	assert.Nil(t, s.OnScore(t0, refOf("t", "r.1", 0), 99, 99))
	assert.Nil(t, s.OnRunCompleted(t0, refOf("t", "r.1", 0), nil))
	assert.Equal(t, 1, s.Stats().NScoreDropped)
	assert.Empty(t, f.calls)
}

func TestUnknownTreeCancelErrors(t *testing.T) {
	s, _ := newSched(testConfig())
	_, err := s.CancelTree(t0, "nope")
	assert.ErrorIs(t, err, ErrUnknownTree)
}

// --- reporting -------------------------------------------------------------

func TestStatsCountByReason(t *testing.T) {
	s, f := newSched(testConfig())
	submit(t, s, t0, "t", selG(t, 1, leavesG(t, 4)...))
	startQueued(s, f, t0)
	busy(s, t0, 1.0)

	st := s.Stats()
	assert.Equal(t, 4, st.NStarts[StartRequiredForQuorum]+st.NStarts[StartSlackRedundancy])
	assert.Equal(t, 3, st.NStops[StopOutrankedBySibling])
	assert.Equal(t, 1, st.NRunning)
}
