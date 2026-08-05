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

// TestBackupStartsOnAnIdleClusterTheProbeCallsBusy covers the case where the
// platform's contention reading and the scheduler's own books disagree: the
// platform reports busy=0.728 while the scheduler holds one slot of four, and
// a straggler's backup attempt must still start rather than sit idle beside
// three empty slots.
//
// Note what it does not assert: that the scheduler stops believing the
// platform. The probe folds committed memory together with CPU before this
// package sees either, so a busy report may mean a slot going to waste or a
// machine whose cores are all spinning, and nothing here can tell which.
// Pressure therefore stays at what the platform said. The backup starts anyway,
// because a stalled incumbent is worth almost nothing and a candidate is worth
// something at any pressure -- which is a stronger property than second-
// guessing the probe would give.
func TestBackupStartsOnAnIdleClusterTheProbeCallsBusy(t *testing.T) {
	s, f := newSched(testConfig())

	// MapReduce's shape: a task per child of the outer Select, each task an
	// inner Select(1, primary, duplicate) so the duplicate may race a
	// straggler. Six leaves against four slots, as in the log.
	pair := func(i int) Group {
		return selG(t, 1,
			leafG(t, fmt.Sprintf("m%d", i)), leafG(t, fmt.Sprintf("m%d-dup", i)))
	}
	submit(t, s, t0, "t", selG(t, 3, pair(0), pair(1), pair(2)))
	startQueued(s, f, t0)
	assert.Len(t, f.starts(), 6, "an idle cluster runs every attempt")

	// The contention burst: six charged against four slots is genuinely full,
	// by the platform's reading and the scheduler's own alike. Each task keeps
	// its primary and gives back its duplicate, which is the "run=1 stops=1"
	// the log shows those leaves holding for the rest of the job.
	f.reset()
	apply(s.OnOccupancy(t0, Occupancy{Busy: 1.0, Slots: 4}))
	assert.Len(t, f.stops(), 3, "one duplicate per task")
	stopped(s, f, t0)
	for i := 0; i < 3; i++ {
		assert.Equal(t, RIdle, nodeView(t, s, "t", NodeID(fmt.Sprintf("r.%d.1", i))).RunState)
	}

	// Two tasks finish. Now one attempt is charged of four slots, and the
	// platform still calls that three-quarters-idle cluster nearly full --
	// which is the exact state the log captured, repeatedly, for three
	// minutes, while nothing was scheduled.
	f.reset()
	for i := 1; i < 3; i++ {
		apply(s.OnRunCompleted(t0, refOf("t", NodeID(fmt.Sprintf("r.%d.0", i)), 0), nil))
	}
	apply(s.OnOccupancy(t0, Occupancy{Busy: 0.728, Slots: 4}))

	// The straggler: running, reporting, converting no time into value.
	apply(s.OnScore(t0, refOf("t", "r.0.0", 0), 0, 0))

	assert.Equal(t, 0.728, s.Stats().Pressure,
		"the platform's reading is taken at face value, as it must be")

	// The backup runs again. Which of the two events restarted it -- capacity
	// coming free, or the straggler admitting it was wedged -- is not the
	// point and is deliberately not asserted: the failure being regressed
	// against is that neither ever did, for the remaining three minutes of the
	// job, on a cluster three-quarters idle.
	if assert.Len(t, f.starts(), 1, "the backup must start") {
		assert.Equal(t, NodeID("r.0.1"), f.starts()[0].ref.Node)
		assert.Equal(t, RunID(1), f.starts()[0].ref.Run, "a fresh attempt")
	}
	assert.Equal(t, 2, nodeView(t, s, "t", "r.0").Target)
	assert.Equal(t, 0.5, s.Stats().SelfOccupancy,
		"the straggler and its backup, two slots of four")
}

// TestWedgedIsRaceableAtFullPressure is the floor invariant. Wmin above zero
// is what keeps a candidate's borrowed tangent worth something when the
// cluster really is full, and without it full pressure would be an admission
// wall again -- continuous this time, but just as absolute as the ExpandAt
// threshold it replaced.
func TestWedgedIsRaceableAtFullPressure(t *testing.T) {
	run := func(t *testing.T, score Score, g Gradient) []call {
		t.Helper()
		s, f := newSched(testConfig())
		busy(s, t0, 1.0)
		submit(t, s, t0, "t", selG(t, 1, leafG(t, "a"), leafG(t, "b")))
		startQueued(s, f, t0)
		f.reset()
		apply(s.OnScore(t0, refOf("t", "r.0", 0), score, g))
		assert.Equal(t, 1.0, s.Stats().Pressure)
		return f.starts()
	}

	t.Run("wedged", func(t *testing.T) {
		// Nothing achieved and nothing being achieved: any positive width at
		// all makes a fresh attempt worth more than this one.
		assert.Len(t, run(t, 0, 0), 1)
	})

	t.Run("healthy", func(t *testing.T) {
		// A full cluster is exactly when redundancy must be refused work that
		// does not need it.
		assert.Empty(t, run(t, 0.5, 1.0))
	})

	t.Run("floor removed", func(t *testing.T) {
		cfg := testConfig()
		cfg.Wmin = 0
		s, f := newSched(cfg)
		busy(s, t0, 1.0)
		submit(t, s, t0, "t", selG(t, 1, leafG(t, "a"), leafG(t, "b")))
		startQueued(s, f, t0)
		f.reset()
		apply(s.OnScore(t0, refOf("t", "r.0", 0), 0, 0))
		assert.Empty(t, f.starts(),
			"with Wmin at zero, full pressure admits nothing at all; the floor "+
				"is the only thing standing between this model and a threshold")
	})
}

func TestPressureSourceSelectsTheFold(t *testing.T) {
	// A platform calling itself full while the scheduler holds nothing is the
	// disagreement the enum exists to resolve. The default is to believe the
	// platform: the folds that do not are available, and documented with why
	// each is a trap.
	for _, tc := range []struct {
		src  PressureSource
		want float64
	}{
		{PressurePlatform, 1},
		{PressureSelf, 0},
		{PressureMax, 1},
		{PressureMin, 0},
	} {
		t.Run(tc.src.String(), func(t *testing.T) {
			cfg := testConfig()
			cfg.PressureSource = tc.src
			// Queue delay is folded in regardless of source; nothing is queued
			// here, so it contributes nothing.
			s, _ := newSched(cfg)
			apply(s.OnOccupancy(t0, Occupancy{Busy: 1.0, Slots: 8}))
			assert.Equal(t, tc.want, s.Stats().Pressure)
		})
	}
}

// TestRaceOnlyWhatIsNotProgressing covers the property redundancy exists for:
// it is spent on work that needs it and withheld from work that does not.
//
// Watch the sign. A gradient is a rate, so an attempt in trouble reports a low
// one and a healthy attempt a high one -- the reverse of what a measure of
// lateness would do, and the reverse of the direction that reads intuitively
// from the phrase "wants a racer".
func TestRaceOnlyWhatIsNotProgressing(t *testing.T) {
	// A pressure high enough that slack alone holds the node at k, so the
	// value comparison is the only thing that can move target.
	const p = 0.52

	newRace := func(t *testing.T) (*Scheduler, *fake) {
		t.Helper()
		s, f := newSched(testConfig())
		busy(s, t0, p)
		submit(t, s, t0, "t", selG(t, 1, leafG(t, "a"), leafG(t, "b")))
		startQueued(s, f, t0)
		return s, f
	}

	t.Run("healthy rate is not raced", func(t *testing.T) {
		s, f := newRace(t)
		f.reset()
		// Half done and covering its work as fast as it expected to: its own
		// tangent projects far past anything a fresh attempt could reach.
		apply(s.OnScore(t0, refOf("t", "r.0", 0), 0.5, 1.0))
		assert.Empty(t, f.starts())
		assert.Equal(t, 1, nodeView(t, s, "t", "r").Target)
	})

	t.Run("wedged is raced", func(t *testing.T) {
		s, f := newRace(t)
		f.reset()
		// Half done and converting no more time into value. Its projection
		// stops at what it already has, and a fresh attempt presumed to run at
		// the nominal rate overtakes it.
		apply(s.OnScore(t0, refOf("t", "r.0", 0), 0.5, 0.0))
		if assert.Len(t, f.starts(), 1) {
			assert.Equal(t, NodeID("r.1"), f.starts()[0].ref.Node)
			assert.Equal(t, StartDerivedValue, f.starts()[0].why.Kind)
		}

		// The winner finishing is what settles the race, and the loser must be
		// stopped as redundancy rather than as ordinary quorum surplus.
		startQueued(s, f, t0)
		f.reset()
		apply(s.OnRunCompleted(t0, refOf("t", "r.0", 0), nil))
		if assert.Len(t, f.stops(), 1) {
			assert.Equal(t, NodeID("r.1"), f.stops()[0].ref.Node)
			assert.Equal(t, StopRacerLost, f.stops()[0].stop.Kind)
		}
	})

	t.Run("nearly done is not raced even at a low rate", func(t *testing.T) {
		s, f := newRace(t)
		f.reset()
		// Crawling, but with almost nothing left to crawl through. What it
		// already holds is worth more than a fresh attempt's whole projection,
		// which is the case a rule reading only the rate would get wrong.
		apply(s.OnScore(t0, refOf("t", "r.0", 0), 5.0, 0.01))
		assert.Empty(t, f.starts())
	})
}

func TestQueuedChildIsNotAPeer(t *testing.T) {
	cfg := testConfig()
	// Take queueing delay out of the pressure fold, so that the only thing
	// left to explain a race is what the running attempt reports.
	cfg.QueueDelayTarget = time.Hour
	s, f := newSched(cfg)
	busy(s, t0, 0.52)
	submit(t, s, t0, "t", selG(t, 1, leafG(t, "a"), leafG(t, "b")))
	f.reset()

	// The first child is started but never observed running, so it has never
	// reported a tangent. However long it sits there it is waiting rather than
	// wedged, and nothing has claimed otherwise.
	late := t0.Add(time.Minute)
	apply(s.Tick(late))
	assert.Empty(t, f.starts())
	assert.Equal(t, 1, nodeView(t, s, "t", "r").Target)

	// Now running and reporting that it is converting no time into value.
	// That is a claim, and it is what justifies a second attempt.
	apply(s.OnRunStarted(late, refOf("t", "r.0", 0)))
	apply(s.OnScore(late, refOf("t", "r.0", 0), 0, 0))
	if assert.Len(t, f.starts(), 1) {
		assert.Equal(t, StartDerivedValue, f.starts()[0].why.Kind)
	}
}

// --- policy invariants -----------------------------------------------------

// TestValueScaleInvariant pins the strongest property this model can have:
// scaling a node's scores and gradients by a common positive factor must
// change nothing.
//
// It is scale invariance rather than invariance to any order-preserving
// transform, and the difference is forced. A gradient is the derivative of the
// curve its score sits on, and a derivative of a mere ordering does not exist,
// so scores have to be cardinal within a node. What scale invariance still
// forbids is reading meaning into a score's magnitude on its own: only the
// scale a node's own reports share is meaningful, never the numbers.
func TestValueScaleInvariant(t *testing.T) {
	trace := func(mul float64) []string {
		s, fk := newSched(testConfig())
		submit(t, s, t0, "t", selG(t, 2, leavesG(t, 6)...))
		startQueued(s, fk, t0)
		for i := 0; i < 6; i++ {
			ref := refOf("t", NodeID(fmt.Sprintf("r.%d", i)), 0)
			apply(s.OnScore(t0, ref, Score(float64(i)*mul), Gradient(float64(i%3)*mul)))
		}
		busy(s, t0, 0.8)
		busy(s, t0, 0.2)
		return fk.trace()
	}

	assert.Equal(t, trace(1), trace(1000))
	assert.Equal(t, trace(1), trace(0.001))
}

// TestNoOscillation pins the anti-oscillation property: pressure sweeping back
// and forth may not make a node retarget on every crossing.
func TestNoOscillation(t *testing.T) {
	cfg := testConfig()
	cfg.ConfirmFor = 5 * time.Second
	// Every proposal here moves the target by one of seven slack slots, well
	// under the fraction that would skip confirmation.
	cfg.JumpFraction = 0.5
	s, f := newSched(cfg)
	submit(t, s, t0, "t", selG(t, 1, leavesG(t, 8)...))
	startQueued(s, f, t0)

	last, changes := nodeView(t, s, "t", "r").Target, 0
	for i := 1; i <= 30; i++ {
		now := t0.Add(time.Duration(i) * time.Second)
		p := 0.55
		if i%2 == 0 {
			p = 0.65
		}
		busy(s, now, p)
		if cur := nodeView(t, s, "t", "r").Target; cur != last {
			changes++
			last = cur
		}
	}
	assert.LessOrEqual(t, changes, 30*int(time.Second)/int(cfg.ConfirmFor)+1,
		"a node may not reverse its target more than once per hold time")
}

// TestAlternatingProposalNeverApplies pins the property that separates a hold
// timer from a cooldown: a proposal that never repeats must never be applied,
// however long it goes on alternating.
//
// A cooldown would not have it. Its window is about how recently the last
// change was made rather than about whether anything consistent is being asked
// for, so a proposal flipping every tick still lands once per window. A hold
// timer restarts whenever the proposal changes, so a node whose value estimate
// will not settle stays exactly where it is, indefinitely.
func TestAlternatingProposalNeverApplies(t *testing.T) {
	cfg := testConfig()
	cfg.ConfirmFor = 5 * time.Second
	cfg.JumpFraction = 2 // nothing here is ever big enough to skip the hold
	s, f := newSched(cfg)
	submit(t, s, t0, "t", selG(t, 1, leavesG(t, 8)...))
	startQueued(s, f, t0)
	settled := nodeView(t, s, "t", "r").Target

	// Alternate faster than the hold time, for many multiples of it.
	for i := 1; i <= 200; i++ {
		now := t0.Add(time.Duration(i) * time.Second)
		p := 0.3
		if i%2 == 0 {
			p = 0.8
		}
		busy(s, now, p)
		assert.Equal(t, settled, nodeView(t, s, "t", "r").Target,
			"a proposal that never repeats must never be applied")
	}
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

// --- cross-tree arbitration ------------------------------------------------

// TestOverShareIsGivenBackToANewTree is what makes arbitration mean anything.
// A tree that arrived first holds the whole cluster, and the only capacity
// there is to give a second one is capacity the first is already using -- so a
// budget that could merely withhold starts would leave the newcomer waiting on
// work that has no reason to end.
func TestOverShareIsGivenBackToANewTree(t *testing.T) {
	s, f := newSchedArb(testConfig(), evenSplit{})
	sized(s, t0, 4)

	submit(t, s, t0, "first", selG(t, 1, leavesG(t, 4)...))
	startQueued(s, f, t0)
	assert.Len(t, f.starts(), 4, "an idle cluster is one tree's until another wants it")

	// Scored out of positional order, so that what gets shed can only have
	// been chosen by rank.
	for i, sc := range []Score{0.1, 0.9, 0.5, 0.7} {
		apply(s.OnScore(t0, refOf("first", NodeID(fmt.Sprintf("r.%d", i)), 0), sc, 0))
	}
	f.reset()

	submit(t, s, t0, "second", selG(t, 1, leavesG(t, 2)...))

	// Half the cluster belongs to the newcomer, so the incumbent hands back
	// two: its two worst, in the order the walk reaches them.
	if assert.Len(t, f.stops(), 2) {
		for _, c := range f.stops() {
			assert.Equal(t, TreeID("first"), c.ref.Tree)
			assert.Equal(t, StopCapacityForHigherTree, c.stop.Kind)
		}
		assert.Equal(t, []NodeID{"r.0", "r.2"},
			[]NodeID{f.stops()[0].ref.Node, f.stops()[1].ref.Node})
	}
	assert.Empty(t, f.starts(), "a stop in flight is not yet a free slot")

	// Only once the stops land is there anything to hand over.
	stopped(s, f, t0)
	starts := f.starts()
	if assert.Len(t, starts, 2) {
		for _, c := range starts {
			assert.Equal(t, TreeID("second"), c.ref.Tree)
		}
	}
	assert.Equal(t, 2, s.Stats().NStops[StopCapacityForHigherTree])
}

// TestArbiterNeverShedsBelowQuorum draws the line the share cannot cross. A
// tree stopped short of its own k has spent everything it still holds for
// nothing, so there is no share small enough to make that trade worth taking.
func TestArbiterNeverShedsBelowQuorum(t *testing.T) {
	s, f := newSchedArb(testConfig(), evenSplit{})
	sized(s, t0, 4)

	submit(t, s, t0, "first", selG(t, 4, leavesG(t, 4)...))
	startQueued(s, f, t0)
	f.reset()

	submit(t, s, t0, "second", selG(t, 1, leavesG(t, 2)...))

	assert.Empty(t, f.stops(), "a tree with no surplus has nothing to give back")
	assert.Empty(t, f.starts(), "and a full cluster has nothing to give either")

	// The newcomer waits for the incumbent to finish rather than for a share
	// it can never be handed. Stating it as a test is what stops a future
	// reading of "its share is two" as licence to break the first tree.
	assert.Equal(t, 4, nodeView(t, s, "first", "r").NRunning)
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
