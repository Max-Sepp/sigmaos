package policy

import "time"

// NodeState is a property of the tree, derived from children and never set
// directly.
type NodeState uint8

const (
	NodePending NodeState = iota
	NodeSatisfied
	NodeFailed
)

func (s NodeState) String() string {
	switch s {
	case NodePending:
		return "pending"
	case NodeSatisfied:
		return "satisfied"
	case NodeFailed:
		return "failed"
	}
	return "unknown"
}

// RunState tracks one attempt at one leaf.
type RunState uint8

const (
	RIdle       RunState = iota // candidate: never run, or requeued
	RQueued                     // start issued, not yet observed running
	RRunning                    // observed running
	RStopping                   // stop issued, terminal event not yet observed
	RSucceeded                  // terminal
	RFailedPerm                 // terminal
)

func (s RunState) String() string {
	switch s {
	case RIdle:
		return "idle"
	case RQueued:
		return "queued"
	case RRunning:
		return "running"
	case RStopping:
		return "stopping"
	case RSucceeded:
		return "succeeded"
	case RFailedPerm:
		return "failed"
	}
	return "unknown"
}

// Charged reports whether a run in this state holds a slot. A slot frees on a
// terminal event for that exact attempt and never on issuing the stop, which
// is why RStopping still counts: until the platform confirms, the work is
// still out there.
func (s RunState) Charged() bool {
	switch s {
	case RQueued, RRunning, RStopping:
		return true
	}
	return false
}

// Terminal reports whether the attempt has ended and no further event for it
// can arrive.
func (s RunState) Terminal() bool {
	return s == RSucceeded || s == RFailedPerm
}

// runEnd records how a leaf's previous attempt ended, which decides how the
// next one is described.
type runEnd uint8

const (
	endNone runEnd = iota
	endStopped
	endFailed
)

// leafState is the mutable half of a leaf. The submitted tree is immutable,
// so everything that changes lives here.
type leafState struct {
	w      Workload
	resume []byte

	// run identifies the current attempt at this leaf and rs is its state.
	// Every requeue bumps run, which is what makes a late event about the
	// previous attempt resolve to nothing in leafFor.
	run RunID
	rs  RunState

	// attempts counts runs that ended badly and is what MaxAttempts bounds.
	// stops counts runs the scheduler chose to end; it is uncapped, because
	// charging a scheduler decision against a leaf's failure budget would
	// make a leaf that is repeatedly paused for capacity permanently dead.
	attempts int
	stops    int
	prev     runEnd

	// startWhy is the provenance a stop is later classified against, and raced
	// records that the attempt was admitted by the value comparison rather
	// than paid for out of slack.
	//
	// They are separate for two reasons. startWhy is overwritten when a retry
	// restates why an attempt is beginning, where how it was admitted is a
	// fact about the decision and does not change. And the distinction matters
	// to a stop: surplus the cluster had going spare, ended because the quorum
	// filled up, is routine, while redundancy admitted on an estimate that it
	// would beat an incumbent has lost a race it was started to run. The tree's
	// shape cannot tell them apart -- both are children past k.
	startWhy StartReasonKind
	raced    bool

	score    Score
	gradient Gradient
	hasScore bool
	scoreAt  time.Time

	queuedAt  time.Time
	startedAt time.Time

	// elapsed is how long this leaf ran, summed over every attempt that has
	// ended. A stopped attempt says nothing about how far it got, so its cost
	// has to be measured here or guessed at by whoever wanted it.
	elapsed time.Duration

	result []byte
}

// node is one node of a live tree.
type node struct {
	id       NodeID
	label    string
	tree     TreeID
	parent   *node
	k        int
	children []*node
	leaf     *leafState

	state NodeState

	target int
	racers int // slots in target that reach past the quorum

	// pending is the target being proposed and pendingSince is when it was
	// first proposed. Together they are the hold timer: a proposal has to stay
	// on the table to be applied, and a changing one restarts its own clock.
	pending      int
	pendingSince time.Time
}

func (n *node) isLeaf() bool { return n.leaf != nil }

type tree struct {
	id        TreeID
	label     string
	attrs     map[string]string
	root      *node
	nodes     map[NodeID]*node
	submitted time.Time
	cancelled bool

	// order is preorder, so iterating it backwards visits every child before
	// its parent and one pass settles the whole tree. Iterating it rather
	// than the map is also what makes reconcile deterministic.
	order []*node
}

// eachLeaf visits every leaf of the subtree rooted at n.
func (n *node) eachLeaf(f func(*node)) {
	if n.isLeaf() {
		f(n)
		return
	}
	for _, c := range n.children {
		c.eachLeaf(f)
	}
}

func (n *node) charged() int {
	c := 0
	n.eachLeaf(func(m *node) {
		if m.leaf.rs.Charged() {
			c++
		}
	})
	return c
}

func (n *node) running() int {
	r := 0
	n.eachLeaf(func(m *node) {
		if m.leaf.rs == RRunning {
			r++
		}
	})
	return r
}

// holding counts children whose subtree holds at least one slot.
//
// It counts children rather than slots because a target is a number of
// children, and the two part company once a child is itself a Select: such a
// child holds as many slots as its own target and still counts once here.
//
// Sizing starts from this rather than from target so that it reads what the
// node has rather than what it last asked for. A target restored from the
// number it was itself derived from would never notice work that was asked for
// and never placed.
func (n *node) holding() int {
	h := 0
	for _, c := range n.children {
		if c.charged() > 0 {
			h++
		}
	}
	return h
}

// reported is whether anything in the subtree has a tangent of its own, which
// is what separates a child standing on its own evidence from one standing on
// a peer's.
func (n *node) reported() bool {
	found := false
	n.eachLeaf(func(m *node) {
		if m.leaf.hasScore {
			found = true
		}
	})
	return found
}

// score is how a subtree ranks against its siblings. A Select stands on its
// best leaf, since that is the one deciding whether it will be satisfied.
func (n *node) score() (Score, bool) {
	var best Score
	found := false
	n.eachLeaf(func(m *node) {
		if l := m.leaf; l.hasScore && (!found || l.score > best) {
			best, found = l.score, true
		}
	})
	return best, found
}

func (n *node) oldestStart() time.Time {
	var t time.Time
	n.eachLeaf(func(m *node) {
		s := m.leaf.startedAt
		if s.IsZero() {
			return
		}
		if t.IsZero() || s.Before(t) {
			t = s
		}
	})
	return t
}

// untried reports whether nothing beneath n has ever been attempted.
func (n *node) untried() bool {
	tried := false
	n.eachLeaf(func(m *node) {
		// A leaf on its first attempt still has run 0, so the state is what
		// separates one that has never been placed from one that is running.
		if m.leaf.run > 0 || m.leaf.rs != RIdle {
			tried = true
		}
	})
	return !tried
}

// probeEligible counts children a probe could still be spent on: alive,
// holding nothing, and never attempted.
//
// A probe is a first look, and a child that has had one has spent it whether
// or not it reported. Were a child stopped before reporting to keep its claim,
// a node could never settle below its slots plus however many children were
// cut short -- it would stop one to make room, admit the last one back, and
// hold there.
//
// The cost is that a node with more children than Slots+Probe only ever looks
// at the first cohort. That is worth knowing but is not this budget's to fix:
// cycling unproven children through a full cluster is a sampling policy, and
// it needs somewhere to put the partial progress it would keep discarding.
func (n *node) probeEligible() int {
	c := 0
	for _, ch := range n.children {
		if ch.state != NodeFailed && ch.charged() == 0 && ch.untried() {
			c++
		}
	}
	return c
}

// alive counts children that have not failed, the ceiling on how many of them
// may run.
func (n *node) alive() int {
	a := 0
	for _, c := range n.children {
		if c.state != NodeFailed {
			a++
		}
	}
	return a
}

// derive recomputes a node's state from its children, or from its run if it
// is a leaf.
func (n *node) derive() NodeState {
	if n.isLeaf() {
		switch n.leaf.rs {
		case RSucceeded:
			return NodeSatisfied
		case RFailedPerm:
			return NodeFailed
		}
		return NodePending
	}
	sat, alive := 0, 0
	for _, c := range n.children {
		switch c.state {
		case NodeSatisfied:
			sat++
			alive++
		case NodePending:
			alive++
		}
	}
	switch {
	case sat >= n.k:
		return NodeSatisfied
	case alive < n.k:
		return NodeFailed
	}
	return NodePending
}

// settle recomputes every node of the tree bottom-up and reports whether
// anything changed.
func (t *tree) settle() bool {
	changed := false
	for i := len(t.order) - 1; i >= 0; i-- {
		n := t.order[i]
		if s := n.derive(); s != n.state {
			n.state = s
			changed = true
		}
	}
	return changed
}

func (t *tree) done() bool {
	return t.root.state != NodePending
}
