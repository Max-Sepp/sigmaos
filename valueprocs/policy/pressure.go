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

// updatePressure folds the platform's reading together with queueing delay
// and smooths the result.
//
// The fold is a max rather than a weighted sum: any one term saturating means
// the cluster is full, and a sum would let an idle-looking term dilute it.
// Saturating the delay term before the max matters because it is an unbounded
// ratio, and a pressure above 1 would drive targets below k.
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

// hysteresis damps a node's target so that a pressure reading hovering near a
// threshold cannot restart work it has just stopped.
//
// Independent per-node expansion under low pressure raises aggregate demand,
// which raises pressure, which contracts every node at once; the deadband and
// the dwell together are what break that loop. The caller still clamps the
// result to at least k, so holding a stale target can never starve required
// work.
func (s *Scheduler) hysteresis(n *node, want int, now time.Time) int {
	if n.lastTargetAt.IsZero() || want == n.target {
		return want
	}
	if now.Sub(n.lastTargetAt) < s.cfg.MinDwell {
		return n.target
	}
	if want > n.target && s.pressure >= s.cfg.ExpandAt {
		return n.target
	}
	if want < n.target && s.pressure <= s.cfg.ContractAt {
		return n.target
	}
	return want
}
