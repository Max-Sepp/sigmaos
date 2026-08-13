package policy

import "fmt"

// What the scheduler is allowed to decide on.
//
// This exists to make the evaluation's central claim testable rather than
// merely stated. The claim is that resource metrics "do not encompass all the
// information a scaling decision could usefully use" -- but every comparison
// that could support it has, until now, changed two things at once: the signal
// the decision is made on, and the entire scheduling stack the decision is
// made in. A difference between a classical application arm and a value-procs
// arm is therefore attributable to either, and the experiment cannot say which.
//
// Selecting the signal inside one engine holds the stack fixed. Both modes
// walk the same trees, size targets the same way, place and shed through the
// same code, and attribute starts with the same reasons. The only difference
// is what value() is allowed to look at.
type Signal int

const (
	// SignalValue is the default: decide on what applications report.
	SignalValue Signal = iota

	// SignalMetrics is the ablation: decide on occupancy alone, ignoring
	// every score and gradient any application reported.
	//
	// This is not a crippled value model but a faithful one of the policy the
	// introduction describes. A scheduler with only resource metrics cannot
	// tell one attempt's work from another's, so it cannot rank them by worth;
	// what it can see is which attempts hold a slot. So every running attempt
	// is worth the same, an attempt that is not running is worth nothing, and
	// the surviving order is by age -- keep what has been running longest,
	// which is what a threshold-driven descheduler does.
	//
	// Two consequences follow, and both are the point rather than a
	// limitation. Nothing is ever admitted on evidence, because no candidate
	// can outrank an incumbent, so the target is whatever pressure alone sizes
	// it to. And a wedged attempt is never raced, because nothing distinguishes
	// it from a healthy one.
	SignalMetrics
)

func (s Signal) String() string {
	switch s {
	case SignalMetrics:
		return "metrics"
	case SignalValue:
		return "value"
	}
	return fmt.Sprintf("Signal(%d)", int(s))
}

// ParseSignal reads a Signal from its name, so a deployment can select the
// mode without a rebuild.
func ParseSignal(s string) (Signal, error) {
	switch s {
	case "", "value":
		return SignalValue, nil
	case "metrics":
		return SignalMetrics, nil
	}
	return SignalValue, fmt.Errorf("policy: unknown signal %q (want \"value\" or \"metrics\")", s)
}

// metricValue is value() under SignalMetrics.
//
// A Select is worth the best of its children, exactly as in the value model,
// so that the two modes differ only in what a leaf is worth and not in how a
// subtree summarizes.
//
// A leaf is worth 1 while it holds a slot and 0 otherwise. The magnitudes do
// not matter, only the two classes: it puts every incumbent above every
// candidate, which makes quorumBar unreachable from below and so leaves
// pressure as the only thing that can size a target. Ranking between
// incumbents then falls through to age, since they all take the same value.
func (s *Scheduler) metricValue(n *node) float64 {
	if !n.isLeaf() {
		best, found := 0.0, false
		for _, c := range n.children {
			if c.state == NodeFailed {
				continue
			}
			if v := s.metricValue(c); !found || v > best {
				best, found = v, true
			}
		}
		return best
	}
	if n.leaf.rs.Charged() {
		return 1
	}
	return 0
}

// signalReported is rank's "has this child evidence of its own" test, made
// signal-aware.
//
// Under SignalMetrics it is always false. That clause exists to keep children
// standing on their own reports ahead of children valued on a borrowed
// tangent, and both of those are notions from the value model; a scheduler
// that never read a report has no such distinction to draw, and letting one
// through here would leak exactly the application signal this mode exists to
// withhold.
func (s *Scheduler) signalReported(n *node) bool {
	if s.cfg.Signal == SignalMetrics {
		return false
	}
	return n.reported()
}
