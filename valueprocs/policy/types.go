// Package policy decides which of an application's interchangeable units of
// work should be running, given how much the application values each one and
// how contended the cluster is.
//
// Work is modelled as k-of-n trees: a Select node is satisfied once k of its
// children are, so the n-k surplus is slack the scheduler may spend when the
// cluster is idle and reclaim when it is busy. Applications report an opaque
// score per running attempt; the scheduler only orders by it.
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

// Score is an opaque measure of how deserving of resources an attempt is;
// higher is better. Only the ordering among children of one Select node is
// meaningful, because only there is one application reporting one metric on
// one scale. Magnitudes and differences carry no information.
type Score float64

// Gradient is the marginal value of adding one more attempt racing the same
// goal: near zero when an attempt is nearly done, large when it is wedged.
// It supplies the extrapolation that must not be inferred from Score.
type Gradient float64

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
