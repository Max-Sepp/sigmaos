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

// TestPrunesToWhatFits is the hyperparameter search: fifteen configurations,
// pruned when the compute to explore them stops being there.
//
// The cluster shrinks to a single slot, so a single candidate is what fits and
// fourteen are given back. What the arm being argued against would do with the
// same fifteen is prune to the same one on a cluster that still had fourteen
// slots free, because a reading past three quarters proposes the quorum
// whatever the slots say -- see TestProportionalArmPrunesOnTheReadingAlone.
//
// Which candidate survives is the half of the decision only the application can
// supply, and it is the same under either rule.
func TestPrunesToWhatFits(t *testing.T) {
	s, f := newSched(testConfig())
	sized(s, t0, 15)
	submit(t, s, t0, "t", selG(t, 1, leavesG(t, 15)...))
	assert.Len(t, f.starts(), 15, "an idle cluster runs every candidate")
	startQueued(s, f, t0)

	// Highest index scores best, so the survivor must not be the first child.
	for i := 0; i < 15; i++ {
		apply(s.OnScore(t0, refOf("t", NodeID(fmt.Sprintf("r.%d", i)), 0), Score(i), 0))
	}
	f.reset()

	apply(s.OnOccupancy(t0, Occupancy{Busy: 1.0, Slots: 1}))
	assert.Len(t, f.stops(), 14)
	assert.Equal(t, 1, nodeView(t, s, "t", "r").Target)

	stopped := map[NodeID]bool{}
	for _, c := range f.stops() {
		stopped[c.ref.Node] = true
		assert.Equal(t, StopOutrankedBySibling, c.stop.Kind)
	}
	assert.False(t, stopped["r.14"], "the best-scoring child must survive")
}

// TestQuorumSurplusShedByScore is the erasure-coded shape: nine workers of which
// six are needed, and the surplus given back when the slots for it go.
func TestQuorumSurplusShedByScore(t *testing.T) {
	s, f := newSched(testConfig())
	sized(s, t0, 9)
	submit(t, s, t0, "t", selG(t, 6, leavesG(t, 9)...))
	startQueued(s, f, t0)
	for i := 0; i < 9; i++ {
		apply(s.OnScore(t0, refOf("t", NodeID(fmt.Sprintf("r.%d", i)), 0), Score(i), 0))
	}
	f.reset()

	apply(s.OnOccupancy(t0, Occupancy{Busy: 1.0, Slots: 6}))
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
// Pressure therefore stays at what the platform said. What changed is that the
// reading is no longer what decides how much runs, so it can be believed and
// still not leave a slot standing empty.
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

	// The contention burst: six charged against four slots really is over
	// capacity, so two of the six have to go and a third follows from the
	// shape -- a duplicate per task is what a pair has to give.
	f.reset()
	apply(s.OnOccupancy(t0, Occupancy{Busy: 1.0, Slots: 4}))
	assert.Len(t, f.stops(), 3, "one duplicate per task")
	stopped(s, f, t0)

	// Three of four slots would then be holding the three primaries, and the
	// fourth is free. It does not stay free: giving back more than had to be
	// given back is the failure this whole rule exists to prevent, so a
	// duplicate goes straight back into the slot the rounding left over.
	assert.Equal(t, 4, s.Stats().NCharged,
		"the shed gives back what does not fit and no more")
	assert.Equal(t, 1.0, s.Stats().SelfOccupancy, "four slots, four attempts")
	startQueued(s, f, t0)

	// Two tasks finish, and their duplicates go with them. Now the straggler's
	// task holds two attempts of four slots, and the platform still calls that
	// half-idle cluster nearly full -- which is the exact state the log
	// captured, repeatedly, for three minutes, while nothing was scheduled.
	f.reset()
	for i := 1; i < 3; i++ {
		apply(s.OnRunCompleted(t0, refOf("t", NodeID(fmt.Sprintf("r.%d.0", i)), 0), nil))
	}
	stopped(s, f, t0)
	apply(s.OnOccupancy(t0, Occupancy{Busy: 0.728, Slots: 4}))

	// The straggler: running, reporting, converting no time into value.
	apply(s.OnScore(t0, refOf("t", "r.0.0", 0), 0, 0))

	assert.Equal(t, 0.728, s.Stats().Pressure,
		"the platform's reading is taken at face value, as it must be")

	// The backup is running. Which event put it there -- capacity coming free,
	// or the straggler admitting it was wedged -- is not the point and is
	// deliberately not asserted: the failure being regressed against is that
	// neither ever did, for the remaining three minutes of the job, on a
	// cluster three-quarters idle.
	dup := nodeView(t, s, "t", "r.0.1")
	assert.True(t, dup.RunState.Charged(), "the backup must be running, got %v", dup.RunState)
	assert.Equal(t, RunID(1), dup.Run, "a fresh attempt")
	assert.Equal(t, 2, nodeView(t, s, "t", "r.0").Target)
	assert.Equal(t, 0.5, s.Stats().SelfOccupancy,
		"the straggler and its backup, two slots of four")
}

// TestWedgedIsRaceableAtFullPressure is the proportional arm's floor invariant.
// Wmin above zero is what keeps a candidate's borrowed tangent worth something
// when the reading says the cluster is full, and without it full pressure would
// be an admission wall again -- continuous this time, but just as absolute as
// the ExpandAt threshold it replaced.
//
// It is stated against that arm because the floor is that arm's problem. Under
// the ledger rule a candidate is always worth Wmax and there is no reading that
// could value it at nothing, so the invariant holds by construction rather than
// by tuning; what bounds racing there is capacity, which is the thing that was
// always meant to bound it.
func TestWedgedIsRaceableAtFullPressure(t *testing.T) {
	run := func(t *testing.T, score Score, g Gradient) []call {
		t.Helper()
		s, f := newSched(proportionalConfig())
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
		cfg := proportionalConfig()
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

// TestPlatformReadingAndOwnBooksAreBothReported pins that the two capacity
// measures stay separate quantities.
//
// They disagree exactly when it matters, and folding them into one scalar --
// which is what the deleted PressureSource enum chose between -- produced a
// number that was neither. A platform reading of 1 on a cluster where this
// scheduler holds nothing means either a slot going to waste or a machine whose
// cores are all spinning on work it did not start, and no fold distinguishes
// them. Reporting both is what lets each rule read the one it needs.
func TestPlatformReadingAndOwnBooksAreBothReported(t *testing.T) {
	s, _ := newSched(testConfig())
	apply(s.OnOccupancy(t0, Occupancy{Busy: 1.0, Slots: 8}))

	st := s.Stats()
	assert.Equal(t, 1.0, st.Pressure, "the platform's reading is taken at face value")
	assert.Equal(t, 0.0, st.SelfOccupancy, "and it holds none of the eight slots")
	assert.Equal(t, 8, st.Slots)
}

// TestRaceOnlyWhatIsNotProgressing covers the property redundancy exists for:
// it is spent on work that needs it and withheld from work that does not.
//
// Watch the sign. A gradient is a rate, so an attempt in trouble reports a low
// one and a healthy attempt a high one -- the reverse of what a measure of
// lateness would do, and the reverse of the direction that reads intuitively
// from the phrase "wants a racer".
//
// Stated against the proportional arm, because it needs a node held at its
// quorum with the comparison as the only thing that could move it. Under the
// ledger rule a free slot is filled by slack before any comparison is reached,
// so the question this test asks does not arise there until the cluster is full
// -- see TestBetterCandidateTakesTheSlotOnAFullCluster.
func TestRaceOnlyWhatIsNotProgressing(t *testing.T) {
	// A pressure high enough that slack alone holds the node at k, so the
	// value comparison is the only thing that can move target.
	const p = 0.52

	newRace := func(t *testing.T) (*Scheduler, *fake) {
		t.Helper()
		s, f := newSched(proportionalConfig())
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
	cfg := proportionalConfig()
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

// --- capacity sizing -------------------------------------------------------

// squeezed is the shape the slack-then-squeeze arm measures: four slots, a
// three-candidate search holding three of them, and another tenant about to
// take most of the machine's memory without taking a slot.
func squeezed(t *testing.T) (*Scheduler, *fake) {
	t.Helper()
	s, f := newSched(testConfig())
	sized(s, t0, 4)
	submit(t, s, t0, "t", selG(t, 1, leavesG(t, 3)...))
	startQueued(s, f, t0)
	if !assert.Equal(t, 3, nodeView(t, s, "t", "r").Target,
		"the search should start wide on an empty cluster") {
		t.FailNow()
	}
	f.reset()
	return s, f
}

// TestBusyMachineWithAFreeSlotDoesNotContract is the regression this rule
// exists for.
//
// Over twenty runs of the arm this reproduces, the search gave up two of its
// three candidates when the squeeze landed and held one for the rest of the
// run, while charged one slot of four. The reading that caused it was another
// tenant's committed memory, so no decision this scheduler could make would
// lower it, and nothing ever grew back.
//
// Nothing about a free slot changed when that tenant arrived, which is what the
// ledger reads and the occupancy ratio never could.
func TestBusyMachineWithAFreeSlotDoesNotContract(t *testing.T) {
	s, f := squeezed(t)

	apply(s.OnOccupancy(t0, Occupancy{Busy: 0.826, Slots: 4}))

	assert.Equal(t, 3, nodeView(t, s, "t", "r").Target,
		"three candidates still fit in four slots, whatever the machine reads")
	assert.Empty(t, f.stops(), "nothing should be given back while a slot is spare")
	assert.Equal(t, 0.826, s.Stats().Pressure,
		"the reading is still believed; it is no longer what sizes the node")
}

// TestProportionalArmPrunesOnTheReadingAlone is the same state under the arm
// being argued against, and is what makes the comparison a comparison.
//
// Same four slots, same three candidates, same reading. The node contracts to
// its quorum with three slots standing empty, because the rule converts the
// reading to a share of the node's slack and 0.826 of two rounds to none.
func TestProportionalArmPrunesOnTheReadingAlone(t *testing.T) {
	s, f := newSched(proportionalConfig())
	sized(s, t0, 4)
	submit(t, s, t0, "t", selG(t, 1, leavesG(t, 3)...))
	startQueued(s, f, t0)
	f.reset()

	apply(s.OnOccupancy(t0, Occupancy{Busy: 0.826, Slots: 4}))

	assert.Equal(t, 1, nodeView(t, s, "t", "r").Target)
	assert.Len(t, f.stops(), 2, "two candidates pruned with a slot already free")
	stopped(s, f, t0)
	assert.Equal(t, 3, s.free(),
		"and having given the slots back the rule will not take them again")
}

// TestContractsOnlyAsFarAsTheSlotsThatWent pins the shape of a real
// contraction. Capacity is reported rather than requested, so a fleet that
// loses a machine leaves this scheduler holding slots that have stopped
// existing -- the one condition under which sizing sheds anything.
//
// What it gives back is the overdraft and not a slot more. The proportional
// rule has no notion of what fits: every reading past three quarters proposes
// the quorum, so losing two slots of four and losing all four are the same
// decision.
func TestContractsOnlyAsFarAsTheSlotsThatWent(t *testing.T) {
	s, f := squeezed(t)

	apply(s.OnOccupancy(t0, Occupancy{Busy: 1.0, Slots: 2}))

	assert.Equal(t, 2, nodeView(t, s, "t", "r").Target,
		"two slots left means two candidates, not the quorum")
	assert.Len(t, f.stops(), 1, "exactly the one candidate that no longer fits")
}

// TestRegrowsWhenSlotsComeBack is the half of the behaviour the proportional
// rule cannot express at all. A contraction was permanent whenever the reading
// that caused it was insensitive to the contraction, which is precisely when it
// was another tenant's.
func TestRegrowsWhenSlotsComeBack(t *testing.T) {
	s, f := squeezed(t)

	apply(s.OnOccupancy(t0, Occupancy{Busy: 1.0, Slots: 2}))
	stopped(s, f, t0)
	assert.Equal(t, 2, nodeView(t, s, "t", "r").Target)

	f.reset()
	apply(s.OnOccupancy(t0, Occupancy{Busy: 1.0, Slots: 4}))

	assert.Equal(t, 3, nodeView(t, s, "t", "r").Target,
		"the slot came back, so the candidate does")
	assert.Len(t, f.starts(), 1)
}

// TestQueuedWorkWithholdsGrowth covers the backstop that replaces the occupancy
// ratio's caution.
//
// A slot ledger cannot see a machine whose cores are spinning on work this
// scheduler did not start, and neither could the ratio -- the probe folds
// memory and CPU together before either is visible here. What can see it is
// this scheduler's own attempts failing to be placed, which is measured from
// inside and cannot be mistaken for someone else's committed memory.
//
// Note the asymmetry: the node keeps what it has. A queue is a reason not to
// take more, never a reason to give back work already under way.
func TestQueuedWorkWithholdsGrowth(t *testing.T) {
	s, f := newSched(testConfig())
	// One slot, so the search starts a single candidate, and that candidate is
	// never placed.
	sized(s, t0, 1)
	submit(t, s, t0, "t", selG(t, 1, leavesG(t, 3)...))
	assert.Len(t, f.starts(), 1)
	assert.Equal(t, RQueued, nodeView(t, s, "t", "r.0").RunState)

	// Slots appear, but this scheduler's own attempt has been waiting longer
	// than the delay target, so they are not slots it can use.
	late := t0.Add(2 * testConfig().QueueDelayTarget)
	f.reset()
	apply(s.OnOccupancy(late, Occupancy{Slots: 4}))

	assert.Equal(t, 1.0, s.Stats().DelayPressure, "the attempt is fully overdue")
	assert.Equal(t, 1, nodeView(t, s, "t", "r").Target,
		"free slots that cannot place work are not headroom")
	assert.Empty(t, f.starts())
	assert.Empty(t, f.stops(), "and a queue never sheds what is already running")
}

// TestBetterCandidateTakesTheSlotOnAFullCluster is where the value model acts
// under the ledger rule.
//
// With slack filling every free slot, a comparison between candidates only
// decides anything once there are none left -- and there it decides by
// displacement rather than by admission, since a full cluster has no slot to
// start an extra attempt into. A candidate that has run before keeps its score
// (see OnRunStopped), so it can outrank a wedged incumbent on evidence and take
// the slot back.
//
// This is the property TestRaceOnlyWhatIsNotProgressing states for the
// proportional arm, restated for a cluster that is actually full rather than
// one that merely reads that way.
func TestBetterCandidateTakesTheSlotOnAFullCluster(t *testing.T) {
	s, f := newSched(testConfig())
	sized(s, t0, 2)
	submit(t, s, t0, "t", selG(t, 1, leavesG(t, 2)...))
	startQueued(s, f, t0)

	// Both report, then the cluster shrinks to a single slot and the weaker one
	// is given back. It keeps its score.
	apply(s.OnScore(t0, refOf("t", "r.0", 0), 0.9, 1.0))
	apply(s.OnScore(t0, refOf("t", "r.1", 0), 0.1, 1.0))
	apply(s.OnOccupancy(t0, Occupancy{Busy: 1.0, Slots: 1}))
	if !assert.Len(t, f.stops(), 1) || !assert.Equal(t, NodeID("r.1"), f.stops()[0].ref.Node) {
		return
	}
	stopped(s, f, t0)
	assert.Equal(t, 0, s.free(), "one slot, and the survivor holds it")

	// Now the survivor wedges: still running, converting no more time into
	// value. The one that was pruned is worth more than it on the evidence both
	// of them reported.
	f.reset()
	apply(s.OnScore(t0, refOf("t", "r.0", 0), 0.9, 0.0))

	if assert.Len(t, f.stops(), 1, "the wedged incumbent gives up the slot") {
		assert.Equal(t, NodeID("r.0"), f.stops()[0].ref.Node)
	}
	stopped(s, f, t0)
	if assert.Len(t, f.starts(), 1, "and the better candidate takes it") {
		assert.Equal(t, NodeID("r.1"), f.starts()[0].ref.Node)
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
		sized(s, t0, 6)
		submit(t, s, t0, "t", selG(t, 2, leavesG(t, 6)...))
		startQueued(s, fk, t0)
		for i := 0; i < 6; i++ {
			ref := refOf("t", NodeID(fmt.Sprintf("r.%d", i)), 0)
			apply(s.OnScore(t0, ref, Score(float64(i)*mul), Gradient(float64(i%3)*mul)))
		}
		// Slots away and back, so the trace contains a shed and a regrowth and
		// the ranking has to decide both.
		apply(s.OnOccupancy(t0, Occupancy{Slots: 3}))
		apply(s.OnOccupancy(t0, Occupancy{Slots: 6}))
		return fk.trace()
	}

	assert.Equal(t, trace(1), trace(1000))
	assert.Equal(t, trace(1), trace(0.001))
}

// TestNoOscillation pins the anti-oscillation property: a capacity signal
// sweeping back and forth may not make a node retarget on every crossing.
//
// Driven from the occupancy reading, since that is the signal with a continuum
// to sweep across; the hold timer it exercises is the same under either rule.
func TestNoOscillation(t *testing.T) {
	cfg := proportionalConfig()
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
	cfg := proportionalConfig()
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
	sized(s, t0, 2)
	submit(t, s, t0, "t", selG(t, 1, leavesG(t, 2)...))
	startQueued(s, f, t0)
	charged := s.Stats().NCharged
	f.reset()

	// One of the two slots goes, so one of the two attempts has to.
	apply(s.OnOccupancy(t0, Occupancy{Busy: 1.0, Slots: 1}))
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

	// One slot for the two of them, so the lower-scoring one is given back.
	sized(s, t0, 1)
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
	sized(s, t0, 1)
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
		sized(s, t0, 1) // one slot: the lower-scoring candidate goes
		apply(s.OnRunStopped(t0, refOf("t", "r.1", RunID(i)), nil))
		sized(s, t0, 2) // and comes straight back when the slot does
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
		sized(s, t0, 3)
		sized(s, t0, 9)
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
	sized(s, t0, 1)
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
	sized(s, t0, 4)
	submit(t, s, t0, "t", selG(t, 1, leavesG(t, 4)...))
	startQueued(s, f, t0)
	apply(s.OnOccupancy(t0, Occupancy{Busy: 1.0, Slots: 1}))

	st := s.Stats()
	assert.Equal(t, 4, st.NStarts[StartRequiredForQuorum]+st.NStarts[StartSlackRedundancy])
	assert.Equal(t, 3, st.NStops[StopOutrankedBySibling])
	assert.Equal(t, 1, st.NRunning)
}
