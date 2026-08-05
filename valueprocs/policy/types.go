// Package policy decides which of an application's interchangeable units of
// work should be running, given how much the application values each one and
// how contended the cluster is.
//
// Work is modelled as k-of-n trees: a Select node is satisfied once k of its
// children are, so the n-k surplus is slack the scheduler may spend when the
// cluster is idle and reclaim when it is busy.
//
// Applications report a tangent per running attempt -- a Score and the
// Gradient of the value curve there -- and every decision this package makes
// is a first-order expansion along one. That is what lets it hold an opinion
// about work that has never run: a candidate's value is read off a running
// peer's tangent, extrapolated over a width set by contention. Which of a
// node's children run, which are shed to pay for a better one, and how far
// past k a node may reach are all the same comparison at different ends.
//
// The package is general to cluster systems and imports only the standard
// library. Two rules keep it deterministic under test: it never reads the
// clock, so every entry point takes the current time, and it never logs
// directly, so callers supply a Logf.
//
// A Scheduler is not safe for concurrent use.
package policy

import (
	"fmt"
	"time"
)

type (
	TreeID string
	NodeID string
	RunID  uint64
)

// RunRef names one attempt at one leaf. It is comparable, so it keys maps on
// both sides of the boundary, and it is the identity a running proc quotes
// back when reporting a score.
type RunRef struct {
	Tree TreeID
	Node NodeID
	Run  RunID
}

func (r RunRef) String() string {
	return fmt.Sprintf("%s/%s/run%d", r.Tree, r.Node, r.Run)
}

// Score is how much of an attempt's value has been realized so far; higher is
// better. It is cardinal within one Select node: differences are meaningful,
// because a Score and the Gradient reported beside it are a point and a slope
// on one curve, and a slope of an ordering does not exist.
//
// Only within a node, though. Two applications, or two Selects, may denominate
// value however they like, so nothing compares a Score across nodes. What this
// package relies on is weaker still: every decision is invariant to rescaling
// one node's scores and gradients together by a common positive factor, since
// scaling a curve scales its slope alike.
type Score float64

// Gradient is the slope of the value curve at the point Score names -- the
// derivative of value with respect to elapsed time, expressed in units of the
// attempt's own expected duration. Reporting (Score, Gradient) is therefore
// reporting a tangent, and
//
//	score + gradient*w
//
// is the application's own estimate of where it will be w expected-durations
// further on.
//
// A healthy attempt reports a gradient near 1: it covers its work in about the
// time it expected to. A stalled one reports near 0, whatever its elapsed time,
// because it is no longer converting time into value. The sign is worth
// stating plainly, since it is the reverse of an overdueness measure: this
// number falls as an attempt gets into trouble.
//
// Normalizing by expected duration is what makes gradients comparable between
// leaves of unlike size within a node, and it is the reason this package can
// extrapolate at all: the extrapolation is the application's, not ours. An
// application that cannot honestly supply the derivative should report 0,
// which claims only that it is making no progress it can account for.
type Gradient float64

// PressureSource selects how the platform's view of contention and the
// scheduler's own books are combined.
//
// They disagree exactly when it matters, and the disagreement has two causes
// that look identical from here. A fleet whose memory is committed to procs
// that are not running reports itself busy while this scheduler holds idle
// slots, and so does a fleet whose cores are all spinning on work this
// scheduler did not start. The first is a slot going to waste; the second is a
// full machine. Nothing in Occupancy.Busy distinguishes them, because the
// platform probe folds memory and CPU together before this package sees
// either.
//
// So the safe reading is the pessimistic one, and the way to still start work
// on a cluster that only looks full is to have something worth starting --
// which is what the value model is for, and it works at any pressure.
type PressureSource uint8

const (
	// PressurePlatform uses only what the platform reports. It is the zero
	// value and the default, because it is the only one of these that measures
	// contention rather than something correlated with it.
	PressurePlatform PressureSource = iota

	// PressureSelf uses charged slots over total slots.
	//
	// Beware what this actually says. It is utilization, not contention: a
	// scheduler using every slot it has on work that is going well reads as
	// fully pressured, and will then start shedding that work. A Select(1, 15)
	// filling four slots on an otherwise empty cluster prunes itself to one
	// candidate under this fold -- which is the opposite of what a search
	// wants, and it would be doing it while nothing else on the machine is
	// asking for anything.
	PressureSelf

	// PressureMax treats either signal saturating as full. Inherits
	// PressureSelf's problem, since a max cannot be lower than either term.
	PressureMax

	// PressureMin believes contention only when the platform and this
	// scheduler's own occupancy agree on it.
	//
	// Tempting where the platform is known to overstate -- it cannot tell
	// committed memory from burning CPU -- but it fails the case that matters:
	// a cluster whose cores are all spinning on other people's work reads as
	// idle for as long as this scheduler holds few slots, so nothing prunes.
	// Prefer to leave the platform's reading alone and let the value model
	// start work despite it, which it can do at any pressure.
	PressureMin
)

func (p PressureSource) String() string {
	switch p {
	case PressureMin:
		return "min"
	case PressurePlatform:
		return "platform"
	case PressureSelf:
		return "self"
	case PressureMax:
		return "max"
	}
	return "unknown"
}

// Workload is one leaf's unit of work. It is supplied at submit, is unchanged
// across attempts, and is never inspected apart from Name.
type Workload interface {
	// Name identifies the workload in log lines. It must be pure and cheap.
	Name() string
}

// Launch is everything one attempt needs to run.
type Launch struct {
	Workload Workload
	Resume   []byte // partial progress from the previous attempt; nil at first
}

// Occupancy is how contended the platform reports itself to be. The platform
// decides how to compute Busy; this package only folds and smooths it.
type Occupancy struct {
	Busy       float64            // [0,1], already saturated by the platform
	Slots      int                // concurrency ceiling
	Components map[string]float64 // diagnostics only; never read by policy
}

// Capacity is how many attempts may be charged at once.
type Capacity struct {
	Slots int
}

// FailureKind separates a run worth retrying from one that never will be.
type FailureKind uint8

const (
	FailTransient FailureKind = iota + 1
	FailPermanent
)

func (k FailureKind) String() string {
	switch k {
	case FailTransient:
		return "transient"
	case FailPermanent:
		return "permanent"
	}
	return "unknown"
}

// TreeSpec is one submitted tree.
type TreeSpec struct {
	ID    TreeID
	Label string
	Root  Group

	// Attrs are the keys an Arbiter may divide capacity on, such as tenant or
	// program. They come from whoever accepted the submission rather than from
	// the application, so that an application cannot raise its own share.
	Attrs map[string]string

	Submitted time.Time
}
