package policy

import (
	"fmt"
	"math"
)

// How contention reaches a decision.
//
// This is the second of the evaluation's two axes and it exists for the same
// reason as Signal: the claim being tested is about what a scheduler is allowed
// to know, and a comparison that changes two things at once cannot say which
// one moved the result. Signal selects what the application is allowed to tell
// the scheduler. Sizing selects what the cluster is allowed to tell it.
//
// Together they name the three arms:
//
//	metrics + proportional  the state of the art: utilisation in, work shed out
//	value   + proportional  application reports, sized on utilisation
//	value   + headroom      application reports, sized on capacity
//
// The middle arm is what makes the split worth having. Without it a difference
// between the first and last is attributable to either half, and the two halves
// fail in opposite directions -- one prunes what it should not have pruned, the
// other prunes the wrong thing.
type Sizing int

const (
	// SizingHeadroom sizes a node on the slot ledger: it keeps what it holds
	// and grows into what is spare, and gives back only what has stopped
	// existing. See afford.
	SizingHeadroom Sizing = iota

	// SizingProportional sizes a node on the occupancy reading, contracting in
	// proportion to it: every surviving child at zero, exactly the quorum at
	// one. This is the policy the introduction describes, and it is kept
	// because the evaluation needs something to argue against, not because
	// anything should run under it.
	//
	// Its defect is that occupancy is a utilisation fraction and the question
	// is a feasibility one. The rule cannot express "would one more fit", so it
	// sheds whether or not the work would have fitted; and where the reading is
	// driven by a tenant this scheduler cannot influence, shedding does not
	// lower it and nothing ever grows back. Rounding makes that concrete: a
	// three-candidate node has three reachable widths, and every reading past
	// three quarters lands on the quorum however much room was left.
	SizingProportional
)

func (s Sizing) String() string {
	switch s {
	case SizingProportional:
		return "proportional"
	case SizingHeadroom:
		return "headroom"
	}
	return fmt.Sprintf("Sizing(%d)", int(s))
}

// ParseSizing reads a Sizing from its name, so a deployment can select the arm
// without a rebuild.
func ParseSizing(s string) (Sizing, error) {
	switch s {
	case "", "headroom":
		return SizingHeadroom, nil
	case "proportional":
		return SizingProportional, nil
	}
	return SizingHeadroom, fmt.Errorf("policy: unknown sizing %q (want \"headroom\" or \"proportional\")", s)
}

// afford is how many of a node's children capacity pays for: the first of the
// two questions retarget asks.
//
// Under SizingHeadroom the measure is the slot ledger rather than the occupancy
// reading, and the two answer different questions. Occupancy says what share of
// the machine is in use. The ledger says whether one more attempt fits, which is
// the only question being asked here, and it says so exactly -- a scheduler
// holding three of four slots has room for one more by the definition of what a
// slot is.
//
// So the shape is deliberately asymmetric. A node keeps what it holds and grows
// into what is spare; it contracts only when the ledger goes negative, which is
// when the slots it holds have stopped existing. That is "give back when not
// giving back would exceed capacity" and nothing weaker. Handing work to another
// tree is a different question and is still shed's, on the arbiter's
// instruction.
//
// The ledger has two halves, and the second is what lets a node be wide before
// it has grounds to be narrow. Slots is what the machines can run. Probe is
// concurrency lent to children nothing has been said about, because a node with
// nothing to rank cannot spend width well and the only thing that changes that
// is running them. A report repays the loan: from then on that child is held
// against Slots, so a node opens at Slots+Probe and closes on Slots as the
// evidence arrives, one child at a time rather than all at once.
//
// Growth is withheld while this scheduler's own attempts cannot be placed.
// Queueing delay is the one term here measured from inside -- it rises because
// work this scheduler started is sitting unplaced -- so it is the honest test of
// whether a spare slot is really spare, where a platform reading cannot tell a
// slot going to waste from a machine that is genuinely gone. It scales growth to
// nothing and never past it: a queue is a reason not to take more, never a
// reason to give back what is already running.
//
// Two units meet here. What is held is a count of children and what is spare is
// a count of slots, and they coincide only while children are leaves. A child
// that is itself a Select costs more than one slot, so on a nested tree this
// reaches optimistically; walk still refuses to start anything the cluster has
// no room for, so what that costs is an ambitious target in a trace rather than
// an oversubscribed machine.
func (s *Scheduler) afford(n *node, k, a int) int {
	if s.cfg.Sizing == SizingProportional {
		return clampInt(k+int(math.Round((1-s.pressure)*float64(a-k))), k, a)
	}
	// settled is unbounded until the platform reports a size, and a node can use
	// no more than it has children in any case. Capping first is also what keeps
	// that unbounded value out of the addition below.
	f := min(s.settled(), a)
	if f > 0 {
		f = int(float64(f) * (1 - s.delay))
	}
	// What the probe budget lends on top, for children there is nothing to say
	// about yet. It only ever adds -- contraction is settled's to express, and
	// letting a spent probe subtract too would charge the same report twice --
	// and a queue withholds it, since a probe buys a child's first report and
	// one that cannot be placed reports nothing.
	p := min(s.probeFree(), n.probeEligible())
	if p > 0 {
		p = int(float64(p) * (1 - s.delay))
	}
	return clampInt(n.holding()+f+p, k, a)
}
