package policy

import (
	"sort"
	"time"
)

func saturate(x float64) float64 {
	switch {
	case x < 0:
		return 0
	case x > 1:
		return 1
	}
	return x
}

// queueDelay is how contended the cluster looks from the one angle only this
// package can see: how long attempts sit between being started and being
// observed running. It costs no round trip, and it measures the thing that
// actually matters, whether work can be placed, rather than a proxy for it.
func (s *Scheduler) queueDelay(now time.Time) float64 {
	var d []time.Duration
	for _, id := range s.order {
		for _, n := range s.trees[id].order {
			if n.isLeaf() && n.leaf.rs == RQueued {
				d = append(d, now.Sub(n.leaf.queuedAt))
			}
		}
	}
	if len(d) == 0 || s.cfg.QueueDelayTarget <= 0 {
		return 0
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	med := d[len(d)/2]
	return saturate(float64(med) / float64(s.cfg.QueueDelayTarget))
}

// selfOccupancy is how full the cluster is by this scheduler's own books:
// slots it holds over slots it was told exist. It is exact, where the
// platform's reading is an inference from memory and CPU, and it is blind to
// everything this scheduler did not start, where the platform's reading is not.
//
// It is reported rather than folded into pressure. The two are not the same
// quantity and averaging them produces neither: SizingHeadroom wants the slots
// themselves, which it takes from free, and SizingProportional wants the
// platform's reading, which is what pressure is.
func (s *Scheduler) selfOccupancy() (float64, bool) {
	if s.occ.Slots <= 0 {
		return 0, false
	}
	return saturate(float64(s.nCharged) / float64(s.occ.Slots)), true
}

// updatePressure folds the platform's reading together with queueing delay,
// then smooths the result.
//
// Queueing delay is folded in with a max: it is the one term measured from
// inside, by how long this scheduler's own attempts sit unplaced, so a cluster
// that cannot place work is full whatever anything else claims. It is saturated
// first, being an unbounded ratio, and a pressure above 1 would drive widths
// below their floor.
//
// There is no choice of source to make. This scheduler's own occupancy is
// reported (see Stats.SelfOccupancy) and is what SizingHeadroom decides on
// directly, in slots rather than as a fraction; folding it into a scalar
// alongside the platform's reading only ever produced a number that was neither
// quantity. Pressure is now what SizingProportional sizes on and what every
// decision is annotated with, and nothing else.
func (s *Scheduler) updatePressure(now time.Time) {
	s.delay = s.queueDelay(now)
	sample := saturate(s.occ.Busy)
	if s.delay > sample {
		sample = s.delay
	}

	a := s.cfg.EWMAAlpha
	switch {
	case !s.seeded:
		s.pressure = sample
		s.seeded = true
	case a <= 0 || a > 1:
		s.pressure = sample
	default:
		s.pressure = a*sample + (1-a)*s.pressure
	}
}

// confirm damps a node's target: a growth big enough to matter is applied at
// once, and every other change has to be proposed continuously for ConfirmFor
// before it takes effect.
//
// It is a hold timer rather than a cooldown, and the distinction decides how
// the scheduler behaves under a changing signal. A cooldown would ask how long
// ago the target last changed and refuse to move again inside a window, which
// leaves a node that has just been adjusted deaf to whatever happens next --
// an attempt that stalls immediately afterwards would have to wait out the
// window before anything could respond. This asks instead how long the current
// proposal has been on the table. Nothing is ever deaf; a proposal simply has
// to persist to be acted on, and one that keeps changing restarts its own clock
// and never lands. Oscillation is damped by instability rather than by recency.
//
// The delay is what breaks a feedback loop: independent per-node expansion
// raises aggregate demand, which raises pressure, which contracts every node at
// once. The caller still clamps the result to at least k, so a held proposal
// can never starve required work.
func (s *Scheduler) confirm(n *node, want int, now time.Time) int {
	if want == n.target {
		n.pending, n.pendingSince = want, time.Time{}
		return want
	}

	// Only growth may skip confirmation, and the asymmetry is deliberate.
	//
	// Adding redundancy is cheap and reversible: an attempt started in error can
	// be stopped, costing the compute it burned meanwhile. Shedding is neither.
	// The work stops, its partial progress is generally lost, and on a Select
	// the child dropped may be the one that would have turned out best -- which
	// no later reconcile can undo, because the evidence that would have
	// overturned the decision is exactly what was cancelled before it could
	// arrive. A search that prunes most of its candidates the instant pressure
	// rises locks in whichever happened to be ahead at that moment, and on a
	// concave progress curve that favours the fastest riser over the best
	// eventual result.
	//
	// So a large change is a strong signal in either direction, but only one
	// direction is safe to act on before it has been confirmed.
	//
	// A jump is measured against the node's slack, since a change of one is the
	// entirety of a two-child node's discretion and a rounding error in a
	// fifteen-child one's. A node with no slack has nothing to confirm.
	// Not, though, when it would take back what this node has just given up.
	//
	// The exemption is for acting on new evidence, and a node that shrank a
	// moment ago has just been told there is no room. Growing back into that
	// is not responsiveness, it is hunting: dropping a child frees the very
	// capacity that then justifies re-adding it, and the cycle repeats at
	// whatever rate the hold allows.
	//
	// A two-child race is where this bites, because slack there is one and any
	// growth is therefore the whole of it -- so the fast-path always fires,
	// re-admission is free, and only the drop is ever paid for. Measured on
	// MapReduce's Select(1, primary, duplicate) reduce tree, that ratchet cost
	// one start and one stop per hold time, indefinitely: 87 cycles in thirty
	// seconds at a one-second hold and 15 at five, all of them work that was
	// begun and thrown away.
	//
	// Once the hold has passed without a shrink the exemption returns, so the
	// first duplicate of a straggler is still admitted at once, which is what
	// the fast-path is for.
	grownBack := !n.shrankAt.IsZero() && now.Sub(n.shrankAt) < s.cfg.ConfirmFor
	if slack := n.alive() - n.k; slack > 0 && want > n.target && !grownBack {
		if float64(want-n.target)/float64(slack) >= s.cfg.JumpFraction {
			n.pending, n.pendingSince = want, time.Time{}
			return want
		}
	}

	// What restarts the clock is a reversal, not a change. A proposal that
	// keeps moving the way the one on the table was already moving is more
	// evidence for the move, not less, and treating each step of it as a fresh
	// proposal is what made a converging node unable to converge.
	//
	// A search opening on fifteen trials narrows one step at a time, because
	// each first report repays one probe and takes exactly one slot back. Under
	// a rule that reset on any change, the proposal was different on every one
	// of those reports and the clock restarted on every one of them, so nothing
	// could be applied until the descent stopped -- which is to say, until the
	// slowest of fifteen attempts had reported. Convergence was hostage to the
	// last straggler rather than paced by the evidence, and on a host running
	// fifteen CPU-bound procs across four cores that straggler is slow for the
	// very reason the narrowing was wanted.
	//
	// Continuing means two things, and both are needed. The proposal must be on
	// the same side of the target, so that crossing it starts again; and it
	// must not have come back towards it, so that a proposal easing off is
	// treated as the change of mind it is. Together they say the case for
	// moving has only strengthened since the clock started.
	//
	// The second half is what keeps an unstable signal from landing anything.
	// A node holding fifteen, offered six and one alternately, is offered a
	// retreat on every other tick, so the clock restarts on every other tick
	// and neither proposal is ever on the table for a whole hold. A descent
	// through fourteen, thirteen, twelve never retreats, so it is not
	// interrupted. Without the second half the alternating case would land the
	// deeper of its two proposals, which is a signal that cannot make up its
	// mind being read as agreement.
	//
	// The elapsed check follows in the same pass rather than waiting for the
	// next reconcile, so that a zero hold time means "apply at once" instead of
	// "apply one reconcile later".
	sameWay := !n.pendingSince.IsZero() &&
		(want > n.target) == (n.pending > n.target) &&
		abs(want-n.target) >= abs(n.pending-n.target)
	if !sameWay {
		n.pendingSince = now
	}
	n.pending = want
	if now.Sub(n.pendingSince) >= s.cfg.ConfirmFor {
		if want < n.target {
			n.shrankAt = now
		}
		n.pending, n.pendingSince = want, time.Time{}
		return want
	}
	return n.target
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
