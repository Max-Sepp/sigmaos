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
	MaxAttempts      int           // per-leaf cap on failed runs; 0 disables
	ScoreStale       time.Duration // silence beyond this is reported as quiet
	ExpandAt         float64       // pressure below which redundancy is added
	ContractAt       float64       // pressure above which redundancy is shed
	MinDwell         time.Duration // least a node holds a target before reversing
	GradientFloor    Gradient      // least gradient that justifies a racer
	QueueDelayTarget time.Duration // queue dwell that reads as full pressure
	EWMAAlpha        float64       // weight on the newest pressure sample
}

// DefaultConfig returns tuning suitable for a cluster of long-running batch
// work.
func DefaultConfig() Config {
	return Config{
		MaxAttempts:      3,
		ScoreStale:       3 * time.Second,
		ExpandAt:         0.55,
		ContractAt:       0.75,
		MinDwell:         5 * time.Second,
		GradientFloor:    0.1,
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

// OnScore records what an attempt reports about itself. It returns nil unless
// the report changes which children of its node should be running, because on
// a busy cluster this is the hottest path in the system.
func (s *Scheduler) OnScore(now time.Time, ref RunRef, sc Score, g Gradient) Effect {
	s.now = now
	n := s.leafFor(ref)
	if n == nil || n.leaf.rs != RRunning {
		s.stats.NScoreDropped++
		return nil
	}
	l := n.leaf
	wasAbove := l.hasScore && l.gradient > s.cfg.GradientFloor
	before := s.topSet(n.parent)

	l.score, l.gradient, l.hasScore, l.scoreAt = sc, g, true, now

	if g > s.cfg.GradientFloor != wasAbove {
		return s.reconcile(now)
	}
	if sameSet(before, s.topSet(n.parent)) {
		return nil
	}
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

	for _, n := range t.order {
		// Wantedness flows down: the root decides its own, a child inherits it.
		isActive := false
		if n.parent == nil {
			isActive = !t.cancelled && n.state == NodePending
			why[n.id] = StartRequiredForQuorum
		} else {
			isActive = active[n.parent.id] && sel[n.id] && n.state == NodePending
		}
		active[n.id] = isActive

		// An interior node spends no capacity; it only sizes and ranks children.
		if !n.isLeaf() {
			if isActive {
				s.retarget(n)
				s.assign(n, sel, why)
			}
			continue
		}

		// Leaves are the only place capacity is claimed or released.
		l := n.leaf
		switch {
		// Charged but unwanted; RStopping is skipped, it was asked once already.
		case !isActive && l.rs.Charged() && l.rs != RStopping:
			kind, best := s.stopReason(t, n)
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

// retarget decides how many of a node's children should be running.
//
// Contention decides how far above k, and score decides which: at pressure 0
// every surviving child runs, at pressure 1 exactly k do. The same arithmetic
// prunes a search, sheds a coded quorum's surplus and races a straggler; only
// k relative to n differs.
func (s *Scheduler) retarget(n *node) {
	a := n.alive()
	if a == 0 {
		n.target, n.racers = 0, 0
		return
	}
	base := clampInt(n.k+int(math.Round((1-s.pressure)*float64(a-n.k))), n.k, a)

	// A racer is justified only when the gradient says duplication would help
	// and there is slack to pay for it. Either alone is wrong: gradient alone
	// races on a full cluster, slack alone races work that is nearly done.
	r := 0
	if s.pressure < s.cfg.ExpandAt {
		for _, c := range n.children {
			if c.state == NodePending && c.running() > 0 && c.gradient() > s.cfg.GradientFloor {
				r++
			}
		}
	}

	want := clampInt(s.hysteresis(n, base+r, s.now), n.k, a)
	if want != n.target {
		n.target = want
		n.lastTargetAt = s.now
	}
	n.racers = max(want-base, 0)
}

// assign marks which children a node keeps running, best first.
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
			why[c.id] = StartSpeculativeRacer
		default:
			why[c.id] = StartSlackRedundancy
		}
	}
}

// rank orders a node's children best first: by score, then by how long they
// have been running so an established attempt is not displaced by a fresh one
// at equal score, then by identity so replay produces the same decisions.
func (s *Scheduler) rank(n *node) []*node {
	out := make([]*node, len(n.children))
	copy(out, n.children)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if af, bf := a.state == NodeFailed, b.state == NodeFailed; af != bf {
			return bf
		}
		as, aok := a.score()
		bs, bok := b.score()
		if aok != bok {
			return aok
		}
		if aok && as != bs {
			return as > bs
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

// topSet is the set of children a node would keep running right now.
func (s *Scheduler) topSet(n *node) map[NodeID]bool {
	if n == nil {
		return nil
	}
	set := make(map[NodeID]bool, n.target)
	for i, c := range s.rank(n) {
		if i >= n.target {
			break
		}
		set[c.id] = true
	}
	return set
}

func sameSet(a, b map[NodeID]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// stopReason attributes a stop to whichever ancestor settled the question.
func (s *Scheduler) stopReason(t *tree, n *node) (StopReasonKind, Score) {
	if t.cancelled {
		return StopTreeCancelled, 0
	}
	for p := n.parent; p != nil; p = p.parent {
		switch p.state {
		case NodeSatisfied:
			// A racing duplicate and an ordinary surplus child look identical
			// from the tree's shape, so only how the attempt began tells them
			// apart.
			if n.leaf.startWhy == StartSpeculativeRacer {
				return StopRacerLost, 0
			}
			return StopQuorumReached, 0
		case NodeFailed:
			return StopTreeUnsatisfiable, 0
		}
	}
	best, _ := n.parent.bestScore()
	return StopOutrankedBySibling, best
}

func (s *Scheduler) start(t *tree, n *node, kind StartReasonKind) func() {
	l := n.leaf
	if kind != StartRequiredForQuorum && kind != StartSpeculativeRacer {
		switch l.prev {
		case endStopped:
			kind = StartRequeuedAfterStop
		case endFailed:
			kind = StartReplacingFailedRun
		}
	}
	why := StartReason{Kind: kind, Pressure: s.pressure, Attempt: l.attempts}
	if kind == StartSpeculativeRacer {
		why.Gradient = l.gradient
	}

	l.rs = RQueued
	l.queuedAt = s.now
	l.startedAt = time.Time{}
	l.startWhy = kind
	s.nCharged++
	s.stats.NStarts[kind]++

	ref := RunRef{Tree: t.id, Node: n.id, Run: l.run}
	lc := Launch{Workload: l.w, Resume: l.resume}
	s.logf("start %v %s: %v", ref, l.w.Name(), why)
	ex := s.ex
	return func() { ex.Start(ref, lc, why) }
}

func (s *Scheduler) stop(t *tree, n *node, kind StopReasonKind, best Score) func() {
	l := n.leaf
	why := StopReason{Kind: kind, Pressure: s.pressure}
	if kind == StopOutrankedBySibling {
		why.Score, why.BestSiblingScore = l.score, best
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
	c, r := 0, 0
	for _, id := range s.order {
		for _, n := range s.trees[id].order {
			if !n.isLeaf() {
				continue
			}
			if n.leaf.rs.Charged() {
				c++
			}
			if n.leaf.rs == RRunning {
				r++
			}
		}
	}
	s.nCharged, s.nRunning = c, r
}

// free is how many more attempts may be charged. A platform that has not
// reported its size yet imposes no ceiling.
func (s *Scheduler) free() int {
	if s.occ.Slots <= 0 {
		return math.MaxInt
	}
	return s.occ.Slots - s.nCharged
}

// budgets asks the arbiter how many further starts each tree may make.
func (s *Scheduler) budgets() map[TreeID]int {
	// Unlimited until an arbiter says otherwise, so every early return below
	// leaves the cluster-wide ceiling as the only one.
	b := make(map[TreeID]int, len(s.trees))
	for _, id := range s.order {
		b[id] = math.MaxInt
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
	for _, sh := range s.arb.Arbitrate(views, Capacity{Slots: s.occ.Slots}) {
		// An arbiter is not trusted to name only trees that exist.
		t, ok := s.trees[sh.Tree]
		if !ok {
			continue
		}
		// A share is a total, but walk spends a remainder, so subtract what the
		// tree already holds. Over its share it gets nothing, never a negative.
		if n := sh.Slots - t.root.charged(); n > 0 {
			b[sh.Tree] = n
		} else {
			b[sh.Tree] = 0
		}
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
