package policy

import "time"

// NodeView is one node's reportable state.
type NodeView struct {
	ID        NodeID
	Label     string
	State     NodeState
	K         int
	NChildren int
	NRunning  int
	NCharged  int
	Target    int

	// Leaf-only fields.
	IsLeaf     bool
	Workload   string
	Run        RunID
	RunState   RunState
	Attempts   int
	Stops      int
	Score      Score
	Gradient   Gradient
	HasScore   bool
	ScoreStale bool
}

// TreeView is one tree's reportable state. It is also what an Arbiter divides
// capacity on, so it carries Attrs.
type TreeView struct {
	ID        TreeID
	Label     string
	Attrs     map[string]string
	State     NodeState
	Cancelled bool
	Submitted time.Time
	NRunning  int
	NCharged  int
	Nodes     []NodeView
}

// Stats is everything needed to explain the scheduler's behaviour from
// outside. A bad pressure reading is the first thing to suspect when the
// layer misbehaves, so the terms it was folded from are reported separately.
type Stats struct {
	Pressure      float64
	Busy          float64
	DelayPressure float64
	Components    map[string]float64

	NTrees   int
	NRunning int
	NCharged int
	Slots    int

	NStarts map[StartReasonKind]int
	NStops  map[StopReasonKind]int

	// NScoreDropped counts score reports for attempts that had already ended.
	// A late report from an in-flight call is expected; a large count is not.
	NScoreDropped int
}

// Stats returns a snapshot. The maps are copies, so a caller may hold them.
func (s *Scheduler) Stats() Stats {
	st := s.stats
	st.Pressure = s.pressure
	st.Busy = s.occ.Busy
	st.DelayPressure = s.delay
	st.NTrees = len(s.trees)
	st.NRunning = s.nRunning
	st.NCharged = s.nCharged
	st.Slots = s.occ.Slots

	st.Components = make(map[string]float64, len(s.occ.Components))
	for k, v := range s.occ.Components {
		st.Components[k] = v
	}
	st.NStarts = make(map[StartReasonKind]int, len(s.stats.NStarts))
	for k, v := range s.stats.NStarts {
		st.NStarts[k] = v
	}
	st.NStops = make(map[StopReasonKind]int, len(s.stats.NStops))
	for k, v := range s.stats.NStops {
		st.NStops[k] = v
	}
	return st
}

// TreeView returns one tree's state, or false if no such tree is registered.
func (s *Scheduler) TreeView(id TreeID) (TreeView, bool) {
	t, ok := s.trees[id]
	if !ok {
		return TreeView{}, false
	}
	return s.treeView(t), true
}

func (s *Scheduler) treeView(t *tree) TreeView {
	v := TreeView{
		ID:        t.id,
		Label:     t.label,
		Attrs:     t.attrs,
		State:     t.root.state,
		Cancelled: t.cancelled,
		Submitted: t.submitted,
		NRunning:  t.root.running(),
		NCharged:  t.root.charged(),
		Nodes:     make([]NodeView, 0, len(t.order)),
	}
	for _, n := range t.order {
		v.Nodes = append(v.Nodes, s.nodeView(n))
	}
	return v
}

func (s *Scheduler) nodeView(n *node) NodeView {
	v := NodeView{
		ID:        n.id,
		Label:     n.label,
		State:     n.state,
		K:         n.k,
		NChildren: len(n.children),
		NRunning:  n.running(),
		NCharged:  n.charged(),
		Target:    n.target,
		IsLeaf:    n.isLeaf(),
	}
	if l := n.leaf; l != nil {
		v.Workload = l.w.Name()
		v.Run = l.run
		v.RunState = l.rs
		v.Attempts = l.attempts
		v.Stops = l.stops
		v.Score = l.score
		v.Gradient = l.gradient
		v.HasScore = l.hasScore
		v.ScoreStale = s.stale(l)
	}
	return v
}

// stale reports whether a running attempt has gone quiet. It is reported and
// never acted on: an attempt that is compute-bound may simply not have
// reached its next reporting point, so silence is not evidence of trouble.
func (s *Scheduler) stale(l *leafState) bool {
	if l.rs != RRunning || s.cfg.ScoreStale <= 0 {
		return false
	}
	since := l.scoreAt
	if !l.hasScore {
		since = l.startedAt
	}
	if since.IsZero() {
		return false
	}
	return s.now.Sub(since) > s.cfg.ScoreStale
}
