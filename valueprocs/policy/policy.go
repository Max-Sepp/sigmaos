package policy

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

var (
	ErrNoTreeID      = errors.New("policy: tree has no id")
	ErrNoRoot        = errors.New("policy: tree has no root")
	ErrDuplicateTree = errors.New("policy: tree already submitted")
	ErrUnknownTree   = errors.New("policy: unknown tree")
)

// Config is the tuning the scheduler runs under.
type Config struct {
	MaxAttempts int           // per-leaf cap on failed runs; 0 disables
	ScoreStale  time.Duration // age at which a tangent counts as fully stale

	// Wmin and Wmax bound how far a candidate may carry a borrowed tangent, in
	// units of expected duration.
	//
	// Only SizingProportional reads both. There Wmin is load-bearing, being
	// what leaves a wedged incumbent raceable on a cluster that reports itself
	// full -- an absolute admission threshold otherwise, wearing a continuous
	// function's clothes. Under SizingHeadroom a candidate is always worth Wmax:
	// contention has already entered the decision once, where it is measured
	// properly, and how much credit to extend an application's own claim is not
	// a second contention question. See width.
	Wmin, Wmax float64

	// Hfresh and Hstale bound how far an incumbent may carry its own tangent,
	// at a just-reported and at a fully stale one.
	Hfresh, Hstale float64

	// NominalRate is the gradient presumed of an attempt nothing has anything
	// to say about. Gradients are normalized by expected duration, so 1 is
	// "proceeds as expected" and is the only defensible prior; it is a
	// parameter because a fleet that habitually overruns its own estimates
	// may honestly presume less.
	NominalRate float64

	// JumpFraction is the share of a node's slack a proposed change must cover
	// to skip confirmation. It is a fraction rather than a count because the
	// same count means opposite things at different widths: moving by one is
	// the whole of a two-child node's slack and a fourteenth of a fifteen-child
	// node's.
	JumpFraction float64

	// ConfirmFor is how long a smaller change must be proposed continuously
	// before it is applied. It is a hold time, not a cooldown: it asks how
	// long this proposal has been on the table, never how recently the last
	// one was applied, so a node is never deaf to what happens next.
	ConfirmFor time.Duration

	QueueDelayTarget time.Duration // queue dwell that reads as full pressure
	EWMAAlpha        float64       // weight on the newest pressure sample

	// Signal is what decisions may be made on: what applications report, or
	// occupancy alone. Sizing is what contention is measured as: the slot
	// ledger, or the occupancy reading. Between them they name the arm (see
	// signal.go and sizing.go); the zero value of each is what this system
	// proposes rather than what it is arguing against.
	Signal Signal
	Sizing Sizing
}

// DefaultConfig returns tuning suitable for a cluster of long-running batch
// work.
func DefaultConfig() Config {
	return Config{
		MaxAttempts:      3,
		ScoreStale:       3 * time.Second,
		Wmin:             0.25,
		Wmax:             2.0,
		Hfresh:           6.0,
		Hstale:           0.35,
		NominalRate:      1.0,
		JumpFraction:     0.5,
		ConfirmFor:       5 * time.Second,
		QueueDelayTarget: 2 * time.Second,
		EWMAAlpha:        0.3,
	}
}

// Scheduler holds every submitted tree and decides what runs. It is not safe
// for concurrent use: callers are expected to serialize entry points
// themselves, which is also what lets an Effect run with no lock held.
type Scheduler struct {
	cfg Config
	ex  RunStartStopper
	arb Arbiter
	log Logf

	trees map[TreeID]*tree
	order []TreeID // submission order, so reconcile is deterministic

	now      time.Time
	occ      Occupancy
	pressure float64
	delay    float64
	seeded   bool

	nCharged int
	nRunning int
	// nChargedReported is how many of the charged attempts have a tangent of
	// their own, which is what separates a slot from a probe.
	nChargedReported int

	stats Stats
}

// NewScheduler returns a scheduler that starts and stops work through ex. A
// nil arb means no per-tree limit and a nil log discards decisions.
func NewScheduler(cfg Config, ex RunStartStopper, arb Arbiter, log Logf) *Scheduler {
	return &Scheduler{
		cfg:   cfg,
		ex:    ex,
		arb:   arb,
		log:   log,
		trees: make(map[TreeID]*tree),
		stats: Stats{
			NStarts: make(map[StartReasonKind]int),
			NStops:  make(map[StopReasonKind]int),
		},
	}
}

func (s *Scheduler) logf(format string, v ...any) {
	if s.log != nil {
		s.log(format, v...)
	}
}

// SubmitTree registers a tree and returns the work its quorum requires.
func (s *Scheduler) SubmitTree(now time.Time, spec TreeSpec) (TreeID, Effect, error) {
	s.now = now
	switch {
	case spec.ID == "":
		return "", nil, ErrNoTreeID
	case spec.Root == nil:
		return "", nil, ErrNoRoot
	}
	if _, dup := s.trees[spec.ID]; dup {
		return "", nil, ErrDuplicateTree
	}
	t, err := buildTree(spec)
	if err != nil {
		return "", nil, err
	}
	if t.submitted.IsZero() {
		t.submitted = now
	}
	s.trees[spec.ID] = t
	s.order = append(s.order, spec.ID)
	return spec.ID, s.reconcile(now), nil
}

// CancelTree stops everything a tree is running. It is idempotent.
func (s *Scheduler) CancelTree(now time.Time, id TreeID) (Effect, error) {
	s.now = now
	t, ok := s.trees[id]
	if !ok {
		return nil, ErrUnknownTree
	}
	if t.cancelled {
		return nil, nil
	}
	t.cancelled = true
	return s.reconcile(now), nil
}

// OnScore records the tangent an attempt reports about itself.
//
// A report that restates what was already known changes nothing and is
// dropped, which is worth the check because on a busy cluster this is the
// hottest path in the system. Anything else reconciles: a tangent is an input
// to every sibling's value as well as its own -- a peer's slope is what a
// candidate borrows -- so there is no cheap local test for whether a report
// mattered.
func (s *Scheduler) OnScore(now time.Time, ref RunRef, sc Score, g Gradient) Effect {
	s.now = now
	n := s.leafFor(ref)
	if n == nil || n.leaf.rs != RRunning {
		s.stats.NScoreDropped++
		return nil
	}
	l := n.leaf
	if l.hasScore && l.score == sc && l.gradient == g {
		l.scoreAt = now
		return nil
	}
	l.score, l.gradient, l.hasScore, l.scoreAt = sc, g, true, now
	return s.reconcile(now)
}

// OnRunStarted reports that an attempt has been placed and is running.
func (s *Scheduler) OnRunStarted(now time.Time, ref RunRef) Effect {
	s.now = now
	n := s.leafFor(ref)
	if n == nil || n.leaf.rs != RQueued {
		return nil
	}
	n.leaf.rs = RRunning
	n.leaf.startedAt = now
	return s.reconcile(now)
}

// OnRunCompleted reports that an attempt finished its work.
func (s *Scheduler) OnRunCompleted(now time.Time, ref RunRef, result []byte) Effect {
	s.now = now
	n := s.leafFor(ref)
	if n == nil || !n.leaf.rs.Charged() {
		return nil
	}
	l := n.leaf
	s.accrue(l)
	l.rs, l.result, l.prev = RSucceeded, result, endNone
	return s.reconcile(now)
}

// OnRunStopped reports that an attempt ended because it was asked to. The
// leaf returns to the candidate pool keeping its last score, so whether it
// ever runs again falls out of the ranking rather than a separate rule.
func (s *Scheduler) OnRunStopped(now time.Time, ref RunRef, partial []byte) Effect {
	s.now = now
	n := s.leafFor(ref)
	if n == nil || !n.leaf.rs.Charged() {
		return nil
	}
	l := n.leaf
	s.accrue(l)
	l.stops++
	l.prev = endStopped
	if len(partial) > 0 {
		l.resume = partial
	}
	s.requeue(l)
	return s.reconcile(now)
}

// OnRunFailed reports that an attempt ended badly. Only failures count
// against MaxAttempts.
func (s *Scheduler) OnRunFailed(now time.Time, ref RunRef, k FailureKind, msg string) Effect {
	s.now = now
	n := s.leafFor(ref)
	if n == nil || !n.leaf.rs.Charged() {
		return nil
	}
	l := n.leaf
	s.accrue(l)
	l.attempts++
	l.prev = endFailed
	s.logf("failed %v %s: %v %s", ref, l.w.Name(), k, msg)
	if k == FailPermanent || (s.cfg.MaxAttempts > 0 && l.attempts >= s.cfg.MaxAttempts) {
		l.rs = RFailedPerm
	} else {
		s.requeue(l)
	}
	return s.reconcile(now)
}

// OnOccupancy records how contended the platform reports itself to be.
func (s *Scheduler) OnOccupancy(now time.Time, o Occupancy) Effect {
	s.now = now
	s.occ = o
	return s.reconcile(now)
}

// Tick advances time. It is the only way queueing delay and staleness are
// noticed while nothing else is happening.
func (s *Scheduler) Tick(now time.Time) Effect {
	s.now = now
	return s.reconcile(now)
}

// accrue adds the attempt that is ending to its leaf's running total.
//
// Every entry point that calls this rejects an event for an attempt that is no
// longer charged, so an attempt is accrued exactly once however many late
// events arrive for it. An attempt never observed running contributes nothing,
// which is right: it held a slot but did no work.
func (s *Scheduler) accrue(l *leafState) {
	if l.startedAt.IsZero() {
		return
	}
	if d := s.now.Sub(l.startedAt); d > 0 {
		l.elapsed += d
	}
}

// elapsed is what a leaf has run in total, counting the attempt in flight.
func (s *Scheduler) elapsed(l *leafState) time.Duration {
	e := l.elapsed
	if l.rs.Charged() && !l.startedAt.IsZero() {
		if d := s.now.Sub(l.startedAt); d > 0 {
			e += d
		}
	}
	return e
}

// requeue returns a leaf to the candidate pool under a fresh run number,
// which is also what makes a late event for the previous attempt droppable.
func (s *Scheduler) requeue(l *leafState) {
	l.rs = RIdle
	l.run++
	l.queuedAt = time.Time{}
	l.startedAt = time.Time{}
}

// Succeeded reports whether this exact attempt is its leaf's current run and
// finished successfully.
//
// It exists so that a caller can tell whether a completion it reported was
// applied or dropped as superseded, which the entry points cannot say for
// themselves: they return the work a decision implies, and a dropped event
// implies none, which is indistinguishable from a decision that changed
// nothing. Anything recording completions on the side has to know which.
func (s *Scheduler) Succeeded(ref RunRef) bool {
	n := s.leafFor(ref)
	return n != nil && n.leaf.rs == RSucceeded
}

// leafFor resolves a reference, requiring an exact run match so that an event
// about an attempt that has already been superseded is ignored.
func (s *Scheduler) leafFor(ref RunRef) *node {
	t, ok := s.trees[ref.Tree]
	if !ok {
		return nil
	}
	n, ok := t.nodes[ref.Node]
	if !ok || !n.isLeaf() || n.leaf.run != ref.Run {
		return nil
	}
	return n
}

// reconcile brings every tree in line with the current pressure and capacity.
func (s *Scheduler) reconcile(now time.Time) Effect {
	s.now = now
	s.updatePressure(now)
	for _, id := range s.order {
		s.trees[id].settle()
	}
	s.account()

	budgets := s.budgets()
	var fs []func()
	for _, id := range s.order {
		fs = append(fs, s.walk(s.trees[id], budgets[id])...)
	}
	s.account()
	return effects(fs)
}

// walk makes every decision for one tree in a single preorder pass. Preorder
// is what makes one pass enough: a node is always visited before its
// children, so the choice of which children to keep running is already made
// by the time they are reached.
func (s *Scheduler) walk(t *tree, budget int) []func() {
	var fs []func()
	active := make(map[NodeID]bool, len(t.order))         // ancestry still wants it
	sel := make(map[NodeID]bool, len(t.order))            // parent kept it among its top target children
	why := make(map[NodeID]StartReasonKind, len(t.order)) // on what grounds
	displaced := make(map[NodeID]bool, len(t.order))      // lost its slot to another tree, not to a sibling

	// One number, both signs meaningful. Positive is headroom to start into;
	// negative is an overdraft, which is what a tree holds after the arbiter
	// divides capacity between more trees than there were before. Giving it
	// back is the only way a tree submitted onto a busy cluster ever runs.
	over := max(-budget, 0)
	budget = max(budget, 0)

	for _, n := range t.order {
		// Wantedness flows down: the root decides its own, a child inherits it.
		isActive := false
		if n.parent == nil {
			isActive = !t.cancelled && n.state == NodePending
			why[n.id] = StartRequiredForQuorum
		} else {
			isActive = active[n.parent.id] && sel[n.id] && n.state == NodePending
			// So does the grounds for losing: a whole subtree handed back is
			// handed back, however its own leaves would have ranked among
			// themselves.
			displaced[n.id] = displaced[n.id] || displaced[n.parent.id]
		}
		active[n.id] = isActive

		// An interior node spends no capacity; it only sizes and ranks children.
		if !n.isLeaf() {
			if isActive {
				s.retarget(n)
				over = max(over-s.released(n), 0)
				over -= s.shed(n, over, displaced)
				s.assign(n, sel, why)
			}
			continue
		}

		// Leaves are the only place capacity is claimed or released.
		l := n.leaf
		switch {
		// Charged but unwanted; RStopping is skipped, it was asked once already.
		case !isActive && l.rs.Charged() && l.rs != RStopping:
			kind, best := s.stopReason(t, n, displaced[n.id])
			fs = append(fs, s.stop(t, n, kind, best))
		// Wanted and idle, if the tree's share and the cluster both allow it.
		case isActive && l.rs == RIdle && budget > 0 && s.free() > 0:
			k := why[n.id]
			if k == 0 {
				k = StartSlackRedundancy
			}
			fs = append(fs, s.start(t, n, k))
			budget--
		}
	}
	return fs
}

// retarget decides how many of a node's children should be running, in two
// steps that answer two different questions.
//
// The first is what the cluster can afford, which afford answers from whichever
// capacity measure the arm selects. This is slack, and it is spent on the best
// children because they are the ones ranked first. No threshold gates it. A rule
// that refused all redundancy above some fixed pressure would leave a stalled
// attempt unraceable on a cluster with slots to spare, since nothing an
// application could report would reach past the threshold.
//
// The second is what the evidence justifies beyond that. Slack is a count and
// so cannot tell one stalled task from nine healthy ones -- it duplicates all
// of them or none. The value comparison is what separates them: a child past
// what slack paid for is admitted only if it is worth more than the weakest
// member of the quorum. On a full cluster that is the only way anything
// redundant starts, and since a stalled incumbent's own tangent values it at
// nearly nothing, it is exactly when one should.
//
// The same two steps prune a search, shed a coded quorum's surplus and race a
// straggler. Nothing here knows which it is doing; only k relative to n and
// what the applications reported differ.
func (s *Scheduler) retarget(n *node) {
	a := n.alive()
	if a == 0 {
		n.target, n.racers, n.pending = 0, 0, 0
		n.pendingSince = time.Time{}
		return
	}

	k := clampInt(n.k, 0, a)
	ranked := s.rank(n)
	base := s.afford(n, k, a)

	// Ranked descending, so once one candidate fails to clear the bar every
	// one after it fails too.
	want := base
	bar := s.quorumBar(ranked, k)
	for i := base; i < a; i++ {
		if s.value(ranked[i]) <= bar {
			break
		}
		want++
	}

	want = clampInt(s.confirm(n, clampInt(want, k, a), s.now), k, a)
	n.target = want
	n.racers = max(want-base, 0)
}

// released is how many charged slots a node's new target already gives up.
//
// Sizing and the arbiter are denominated in the same slots and, for a lone
// tree, measure the same deficit: its share is the whole cluster, so a
// shrinking cluster shows up once in afford and again in the budget. A node
// asked to pay it twice pays down to its quorum, which is the difference
// between a search of four trials and a search of one. So shed is owed only
// whatever sizing did not already surrender.
func (s *Scheduler) released(n *node) int {
	ranked, keep := s.rank(n), 0
	for i := 0; i < n.target && i < len(ranked); i++ {
		keep += ranked[i].charged()
	}
	return max(n.charged()-keep, 0)
}

// shed shrinks a node's target to hand back capacity the arbiter has given to
// another tree, and reports how many slots that releases.
//
// Only slack goes. A node never drops below k, because a tree held short of
// its own quorum has spent everything it still holds for nothing, so there is
// no share small enough to make that trade worth taking; a tree whose required
// work alone exceeds its share simply has nothing to give and keeps running.
//
// Within a node the lowest-valued children go first, on the same ranking
// retarget admitted down, so what a node gives back is whatever it reached
// furthest to get. Between nodes it is preorder, so the broadest redundancy is
// given up before the deepest -- and dropping one child of a Select can free a
// whole subtree, which is why this counts slots freed rather than children
// dropped.
func (s *Scheduler) shed(n *node, over int, displaced map[NodeID]bool) int {
	if over <= 0 || n.target <= n.k {
		return 0
	}
	ranked := s.rank(n)
	freed, dropped := 0, 0
	for n.target > n.k && freed < over {
		c := ranked[n.target-1]
		n.target--
		dropped++
		displaced[c.id] = true
		// A child holding nothing frees nothing, and the loop keeps going.
		// That costs the tree only work it was in no position to start, since
		// a tree over its share has no budget to start anything with.
		freed += c.charged()
	}
	if dropped == 0 {
		return 0
	}
	n.racers = max(n.racers-dropped, 0)
	// Put the smaller target on the table as the proposal being held, so that
	// retarget on the next reconcile has to confirm restoring it rather than
	// simply undoing the arbiter.
	n.pending, n.pendingSince = n.target, s.now
	return freed
}

// assign marks which children a node keeps running, best first.
//
// Everything inside the quorum is required. Everything past it is redundancy,
// and which of the two surplus reasons applies is a question about what paid
// for the slot: slack the cluster had going spare, or a value estimate that
// cleared the quorum bar. Telling them apart afterwards is the only way to
// know whether the model decided anything or merely spent idle capacity.
func (s *Scheduler) assign(n *node, sel map[NodeID]bool, why map[NodeID]StartReasonKind) {
	for i, c := range s.rank(n) {
		if c.state == NodeFailed || i >= n.target {
			sel[c.id] = false
			continue
		}
		sel[c.id] = true
		switch {
		case i < n.k:
			why[c.id] = StartRequiredForQuorum
		case i >= n.target-n.racers:
			why[c.id] = StartDerivedValue
		default:
			why[c.id] = StartSlackRedundancy
		}
	}
}

// rank orders a node's children best first, by the one value both ends of the
// decision read: retarget admits down this order and shed drops up it, so
// whatever a node reached furthest to get is the first thing it gives back,
// without either end having to agree about anything except this function.
//
// Children that have reported come first as a class, ahead of children valued
// on a peer's borrowed tangent however well that tangent speaks of them. An
// estimate is evidence about what a candidate might become and a report is
// evidence about what an attempt is, and the two do not belong in one
// ordering: interleaving them would let a guess displace work already under
// way, and would collapse the redundancy comparison in quorumBar into a
// comparison of a value with itself.
//
// Ties break towards work already under way, then by identity, so that
// replaying a tree produces the same decisions.
func (s *Scheduler) rank(n *node) []*node {
	out := make([]*node, len(n.children))
	copy(out, n.children)
	val := make(map[NodeID]float64, len(out))
	for _, c := range out {
		val[c.id] = s.value(c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if af, bf := a.state == NodeFailed, b.state == NodeFailed; af != bf {
			return bf
		}
		if ar, br := s.signalReported(a), s.signalReported(b); ar != br {
			return ar
		}
		if av, bv := val[a.id], val[b.id]; av != bv {
			return av > bv
		}
		at, bt := a.oldestStart(), b.oldestStart()
		if !at.Equal(bt) {
			switch {
			case at.IsZero():
				return false
			case bt.IsZero():
				return true
			}
			return at.Before(bt)
		}
		return a.id < b.id
	})
	return out
}

// stopReason attributes a stop to whichever ancestor settled the question.
func (s *Scheduler) stopReason(t *tree, n *node, displaced bool) (StopReasonKind, float64) {
	if t.cancelled {
		return StopTreeCancelled, 0
	}
	for p := n.parent; p != nil; p = p.parent {
		switch p.state {
		case NodeSatisfied:
			// Surplus the cluster had going spare and redundancy started to
			// beat an incumbent look identical from the tree's shape, so only
			// how the attempt was admitted tells them apart.
			if n.leaf.raced {
				return StopRacerLost, 0
			}
			return StopQuorumReached, 0
		case NodeFailed:
			return StopTreeUnsatisfiable, 0
		}
	}
	// Nothing about the tree settled it, so the attempt lost its place while
	// still wanted. Either its own tree was made to give the slot up, or a
	// sibling simply outranked it -- and only the second says anything about
	// this leaf, so only the second is worth quoting a ranking for.
	if displaced {
		return StopCapacityForHigherTree, 0
	}
	return StopOutrankedBySibling, s.bestSiblingValue(n)
}

// bestSiblingValue is the best value among a node's siblings, the figure a
// stop for being outranked is measured against.
func (s *Scheduler) bestSiblingValue(n *node) float64 {
	if n.parent == nil {
		return 0
	}
	best, found := 0.0, false
	for _, c := range n.parent.children {
		if c == n || c.state == NodeFailed {
			continue
		}
		if v := s.value(c); !found || v > best {
			best, found = v, true
		}
	}
	return best
}

func (s *Scheduler) start(t *tree, n *node, kind StartReasonKind) func() {
	l := n.leaf
	// Whether the value comparison admitted this attempt is settled before the
	// retry kinds overwrite why it is starting, because the two answer
	// different questions and a stop later needs the first one.
	raced := kind == StartDerivedValue
	if kind != StartRequiredForQuorum && kind != StartDerivedValue {
		switch l.prev {
		case endStopped:
			kind = StartRequeuedAfterStop
		case endFailed:
			kind = StartReplacingFailedRun
		}
	}
	why := StartReason{Kind: kind, Pressure: s.pressure, Attempt: l.attempts}
	if kind == StartDerivedValue {
		why.Value, why.Width = s.value(n), s.width()
	}

	l.rs = RQueued
	l.queuedAt = s.now
	l.startedAt = time.Time{}
	l.startWhy = kind
	l.raced = raced
	s.nCharged++
	s.stats.NStarts[kind]++

	ref := RunRef{Tree: t.id, Node: n.id, Run: l.run}
	lc := Launch{Workload: l.w, Resume: l.resume}
	s.logf("start %v %s: %v", ref, l.w.Name(), why)
	ex := s.ex
	return func() { ex.Start(ref, lc, why) }
}

func (s *Scheduler) stop(t *tree, n *node, kind StopReasonKind, best float64) func() {
	l := n.leaf
	why := StopReason{Kind: kind, Pressure: s.pressure}
	if kind == StopOutrankedBySibling {
		why.Value, why.BestSiblingValue = s.value(n), best
	}

	// The slot stays charged: until a terminal event arrives the work really
	// is still out there, and handing the slot to someone else now would
	// promise the same capacity twice.
	l.rs = RStopping
	s.stats.NStops[kind]++

	ref := RunRef{Tree: t.id, Node: n.id, Run: l.run}
	s.logf("stop %v %s: %v", ref, l.w.Name(), why)
	ex := s.ex
	return func() { ex.Stop(ref, why) }
}

// account recounts what is charged and what is running. Its only job is to
// stop the same freed slot being promised to two claimants; it is not a model
// of the cluster, which is what pressure is for.
func (s *Scheduler) account() {
	c, r, rep := 0, 0, 0
	for _, id := range s.order {
		for _, n := range s.trees[id].order {
			if !n.isLeaf() {
				continue
			}
			if n.leaf.rs.Charged() {
				c++
				if n.leaf.hasScore {
					rep++
				}
			}
			if n.leaf.rs == RRunning {
				r++
			}
		}
	}
	s.nCharged, s.nRunning, s.nChargedReported = c, r, rep
}

// free is how many more attempts may be charged at all, against the machines'
// ceiling and the probe budget together; a platform that has not reported its
// size imposes neither. It is the only limit that refuses a start -- sizing
// asks a different question and reads settled.
func (s *Scheduler) free() int {
	if s.occ.Slots <= 0 {
		return math.MaxInt
	}
	return s.occ.Slots + s.occ.Probe - s.nCharged
}

// probeHeld is how many charged attempts the budget is carrying. Capping at
// the budget is what makes a zero budget mean what it says: an attempt is
// probe-funded only if a probe was there to fund it, and is held against Slots
// past that however little it has said.
func (s *Scheduler) probeHeld() int {
	return min(s.nCharged-s.nChargedReported, s.occ.Probe)
}

// settled is capacity net of what is held against it, and is what a node is
// sized to. Probe-funded attempts are left out, so each report converts a
// probe back into a slot and narrows the node by one.
func (s *Scheduler) settled() int {
	if s.occ.Slots <= 0 {
		return math.MaxInt
	}
	return s.occ.Slots - (s.nCharged - s.probeHeld())
}

// probeFree is how much of the probe budget is unspent.
func (s *Scheduler) probeFree() int {
	return s.occ.Probe - s.probeHeld()
}

// budgets asks the arbiter how many further starts each tree may make.
func (s *Scheduler) budgets() map[TreeID]int {
	// Unlimited until an arbiter says otherwise, so every early return below
	// leaves the cluster-wide ceiling as the only one.
	b := make(map[TreeID]int, len(s.trees))
	for _, id := range s.order {
		b[id] = Unbounded
		s.trees[id].share, s.trees[id].budget = Unbounded, Unbounded
	}
	if s.arb == nil {
		return b
	}
	// Only trees that could still use capacity are worth dividing between.
	views := make([]TreeView, 0, len(s.order))
	for _, id := range s.order {
		if t := s.trees[id]; !t.cancelled && !t.done() {
			views = append(views, s.treeView(t))
		}
	}
	if len(views) == 0 {
		return b
	}
	// Probes are included, so a tree running wide on work that has not reported
	// yet is not read as overdrafted and made to give back what it was lent.
	for _, sh := range s.arb.Arbitrate(views, Capacity{Slots: s.occ.Slots + s.occ.Probe}) {
		// An arbiter is not trusted to name only trees that exist.
		t, ok := s.trees[sh.Tree]
		if !ok {
			continue
		}
		// A share is a total, but walk spends a remainder, so subtract what the
		// tree already holds. The difference is signed on purpose: below zero
		// it is what the tree has to give back, and clamping it away is what
		// would let the first tree to arrive keep a full cluster to itself.
		b[sh.Tree] = sh.Slots - t.root.charged()
		t.share, t.budget = sh.Slots, b[sh.Tree]
	}
	return b
}

func clampInt(v, lo, hi int) int {
	switch {
	case v < lo:
		return lo
	case v > hi:
		return hi
	}
	return v
}

// buildTree turns an immutable spec into the live tree that carries state.
func buildTree(spec TreeSpec) (*tree, error) {
	t := &tree{
		id:        spec.ID,
		label:     spec.Label,
		attrs:     spec.Attrs,
		submitted: spec.Submitted,
		share:     Unbounded,
		budget:    Unbounded,
		nodes:     make(map[NodeID]*node),
	}
	root, err := t.build(spec.Root, nil, "r")
	if err != nil {
		return nil, err
	}
	t.root = root
	return t, nil
}

// build assigns each node an identity derived from its path, so that a score
// report can name a node and replaying a tree produces the same names.
func (t *tree) build(g Group, parent *node, id NodeID) (*node, error) {
	n := &node{id: id, label: g.label(), tree: t.id, parent: parent}
	t.nodes[id] = n
	t.order = append(t.order, n) // preorder: parents before children

	switch v := g.(type) {
	case leaf:
		n.leaf = &leafState{w: v.w}
	case selectBestK:
		n.k = v.k
		for i, c := range v.children {
			cn, err := t.build(c, n, NodeID(fmt.Sprintf("%s.%d", id, i)))
			if err != nil {
				return nil, err
			}
			n.children = append(n.children, cn)
		}
	default:
		return nil, ErrUnknownKind
	}
	return n, nil
}
