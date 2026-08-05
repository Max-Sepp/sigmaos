package policy

import "math"

// The value model: what an attempt is worth, and what one that has never run
// would be worth.
//
// Everything here is a first-order expansion of a tangent an application
// reported (see Gradient). Two widths bound how far a tangent may be carried,
// and they are separate because they answer different questions. An
// incumbent's horizon asks how much of its own claim still stands, and shrinks
// as its report ages. A candidate's width asks how much speculative credit the
// cluster can afford it, and shrinks as contention rises. Neither ever reaches
// zero.

// width is how far a candidate's borrowed tangent may be carried, in units of
// expected duration.
//
// The floor matters more than the slope. Were Wmin zero, full pressure would
// value every candidate at nothing, and no evidence of any kind could then
// justify starting one -- an absolute admission threshold wearing a continuous
// function's clothes. Holding Wmin above zero means there is always an
// incumbent slow enough to be worth overtaking, however contended the cluster.
func (s *Scheduler) width() float64 {
	lo, hi := s.cfg.Wmin, s.cfg.Wmax
	if hi < lo {
		hi = lo
	}
	return lo + (hi-lo)*(1-saturate(s.pressure))
}

// horizon is how far an incumbent's own tangent may be carried.
//
// It shrinks with staleness because a tangent describes the curve at the point
// it was taken, and an attempt that has not reported since has probably left
// that point. This discounts; it never terminates. A quiet leaf loses the
// argument against a candidate with something to show, and that is all -- it
// is never stopped for being quiet, which would punish an attempt that is
// merely compute-bound between reporting points.
func (s *Scheduler) horizon(l *leafState) float64 {
	lo, hi := s.cfg.Hstale, s.cfg.Hfresh
	return hi + (lo-hi)*s.staleness(l)
}

// staleness is how far a leaf's tangent has aged, from 0 at a fresh report to
// 1 at ScoreStale and beyond.
func (s *Scheduler) staleness(l *leafState) float64 {
	if s.cfg.ScoreStale <= 0 {
		return 0
	}
	since := l.scoreAt
	if since.IsZero() {
		since = l.startedAt
	}
	if since.IsZero() {
		// Never ran and never reported: nothing has aged, because nothing was
		// ever claimed. The leaf has no tangent of its own and is valued off a
		// peer's instead.
		return 0
	}
	d := s.now.Sub(since)
	if d <= 0 {
		return 0
	}
	return saturate(float64(d) / float64(s.cfg.ScoreStale))
}

// value is what a node is worth: for a leaf, where its tangent says it will
// get to; for a Select, the best its children can do, since that is the child
// deciding whether it is satisfied.
//
// Two leaves are valued on different widths on purpose. One that has reported
// stands on its own claim, carried over its own horizon. One that has not is a
// candidate, and stands on a peer's claim carried over the candidate's width --
// which is smaller, and shrinks under contention, because it is borrowed.
func (s *Scheduler) value(n *node) float64 {
	if !n.isLeaf() {
		best, found := 0.0, false
		for _, c := range n.children {
			if c.state == NodeFailed {
				continue
			}
			if v := s.value(c); !found || v > best {
				best, found = v, true
			}
		}
		return best
	}

	l := n.leaf
	if l.hasScore {
		return float64(l.score) + float64(l.gradient)*s.horizon(l)
	}
	return s.derived(n)
}

// derived values a leaf that has never reported, by borrowing the tangent of a
// running peer.
//
// This is what lets the scheduler hold an opinion about work that has not run.
// Absent it a candidate would be worth nothing, so nothing could ever justify
// starting one, and a duplicate could run only when some count happened to
// allow it.
//
// The slope is borrowed and the starting point is always nothing, because only
// a leaf that has never reported reaches here. One that has -- including one
// stopped and requeued, which keeps its score -- stands on its own tangent
// instead, discounted by however stale that tangent has gone. So there is no
// case here for resuming from partial progress: a leaf with progress to resume
// from is a leaf with a report, and it never gets here.
func (s *Scheduler) derived(n *node) float64 {
	rate, ok := s.peerRate(n)
	if !ok {
		// Nothing running has anything to say. This is the common case for a
		// node holding the only attempt at its goal: a two-child Select has no
		// peer that is not the incumbent the candidate would be racing, and
		// that one is excluded below.
		//
		// Normalizing gradients by expected duration is what supplies an
		// answer anyway. A rate of 1 means "covers its work in the time it
		// expected to", so 1 is what a fresh attempt is presumed to manage
		// until something says otherwise. The prior exists only because the
		// units were chosen to make one meaningful; without it a lone
		// unproductive attempt could never be raced at all.
		rate = s.cfg.NominalRate
	}
	return rate * s.width()
}

// peerRate finds the tangent to value a candidate with: the running attempt
// whose report point is nearest the candidate's own starting point, searching
// its own Select first and widening to enclosing Selects until one is found.
//
// Nearest rather than best, because a first-order expansion is only good near
// where it was taken -- borrowing the tangent of a peer at a wholly different
// point on the curve claims more than that peer ever said.
//
// Two things are left out. The candidate's own subtree, which is trivial. And
// the strongest running attempt in the candidate's own Select, which is not:
// that is what the candidate's admission is measured against, and in a
// two-child Select it is necessarily the incumbent the candidate would be
// racing. Valuing a duplicate off the tangent of the very attempt it exists to
// overtake would conclude that a replacement for an unproductive attempt is
// worth exactly as little as the unproductive attempt, so no such attempt
// could ever be raced.
//
// That exclusion holds at every scope, not only the innermost. Widening the
// search does not stop the incumbent being the incumbent -- it is still among
// the enclosing group's leaves, and readmitting it one level up reintroduces
// the same circularity. What widening does is bring siblings working on other
// goals into view, which is what a narrow Select has instead of peers of its
// own; a wide Select has them from the start.
func (s *Scheduler) peerRate(cand *node) (float64, bool) {
	// A candidate has done nothing, so the point of the curve it is about to
	// sit at is the origin, and the nearest tangent to it is the one taken
	// lowest -- the peer that has itself progressed least. That is the peer
	// whose next stretch of work most resembles the candidate's first.
	const start = 0.0
	skip := strongestRunning(cand.parent, cand)

	for scope := cand.parent; scope != nil; scope = scope.parent {
		var (
			rate  float64
			near  float64
			found bool
		)
		scope.eachLeaf(func(m *node) {
			if m == cand || (skip != nil && under(m, skip)) {
				return
			}
			l := m.leaf
			if l.rs != RRunning || !l.hasScore {
				return
			}
			if d := math.Abs(float64(l.score) - start); !found || d < near {
				rate, near, found = float64(l.gradient), d, true
			}
		})
		if found {
			return rate, true
		}
	}
	return 0, false
}

// strongestRunning is the best-scoring running child of n other than excl. It
// reads scores rather than values on purpose: values are what rank is built
// from and rank is what asks for this, so consulting them here would be
// circular.
func strongestRunning(n, excl *node) *node {
	if n == nil {
		return nil
	}
	var (
		best  *node
		score Score
	)
	for _, c := range n.children {
		if c == excl || c.state == NodeFailed || c.running() == 0 {
			continue
		}
		sc, ok := c.score()
		if !ok {
			continue
		}
		if best == nil || sc > score {
			best, score = c, sc
		}
	}
	return best
}

// under reports whether m is n or lies beneath it.
func under(m, n *node) bool {
	for p := m; p != nil; p = p.parent {
		if p == n {
			return true
		}
	}
	return false
}

// quorumBar is the value a redundant child has to beat to be worth admitting:
// that of the weakest member of the quorum, ranked[k-1].
//
// A child past the quorum earns its slot only by standing in for one inside
// it, and the one it would stand in for is the weakest -- so that is what it
// is measured against. Comparing it against the whole node's best would refuse
// every redundancy that was not also an improvement on work already going
// well, and comparing it against the node's worst would admit anything at all
// once a single child was struggling.
//
// This is why rank puts children that have reported ahead of children valued
// on a borrowed tangent, whatever the two numbers say. Were they interleaved,
// a candidate estimated above an incumbent would sort above it, the bar would
// become the candidate's own value, and the comparison would compare a thing
// with itself. Keeping them apart is what makes the question "is this
// redundancy worth it" rather than "which of these two should run" -- and it
// means an established attempt is raced, never silently swapped out for a
// guess about a fresh one.
func (s *Scheduler) quorumBar(ranked []*node, k int) float64 {
	if k <= 0 || k > len(ranked) {
		return 0
	}
	return s.value(ranked[k-1])
}
