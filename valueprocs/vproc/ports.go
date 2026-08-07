package vproc

import (
	"context"
	"sync/atomic"

	"sigmaos/sigmaclnt"
)

// Scorer is the value channel, and the whole of what an application tells the
// scheduler about work in flight.
//
// A score is opaque and higher means more deserving of resources: only its
// ordering against the proc's siblings is ever used. The gradient beside it is
// the marginal value of one more proc racing this one to the same goal, which
// exists because the scheduler is forbidden to extrapolate one of its own.
//
// It is what a work loop holds. Reporting is coalesced, so a caller may report
// every iteration and must not report rarely to be kind. A proc that nothing
// scheduled still has a Scorer, whose scores go nowhere.
type Scorer interface {
	// Score reports how the work is going.
	Score(score, gradient float64)
}

// Stopper is how work in flight learns it has been asked to stop.
//
// Only a proc started with WithGracefulEvict is ever told; otherwise it is
// killed outright, which is safe because leaves are idempotent. The four forms
// are one fact for four shapes of caller: a poll, an inner loop that must not
// pay for a call, a select, and something that takes a context.
type Stopper interface {
	// Cancelled reports whether this proc has been asked to stop.
	Cancelled() bool

	// CancelledFlag is Cancelled for code that takes a flag.
	CancelledFlag() *atomic.Bool

	// Done closes when this proc has been asked to stop.
	Done() <-chan struct{}

	// Context is Done for code that takes a context.
	Context() context.Context
}

// Exiter is the one exit a value proc is allowed.
//
// Exactly one of these happens, once, and the result travels to whoever
// submitted the tree, who cannot collect it otherwise. Which one is called
// separates an attempt that will be retried from a leaf that is finished for
// good, so a component handed an Exiter is handed that decision.
type Exiter interface {
	// Complete reports the work finished, with a result to hand back.
	Complete(result any)

	// StoppedWith reports the proc stopped because it was asked to, handing
	// back what it had. Only meaningful under WithGracefulEvict.
	StoppedWith(partial any)

	// Fail reports an attempt that went wrong and is worth trying again.
	Fail(err error)

	// Fatal reports work that will never succeed, however often it is run.
	Fatal(err error)
}

// Runtime is everything Start hands back. Name it when a component is given
// the whole runtime; prefer one of the three above when it is not.
type Runtime interface {
	Scorer
	Stopper
	Exiter

	// SigmaClnt returns the underlying client.
	SigmaClnt() *sigmaclnt.SigmaClnt

	// NodeID is which leaf of its tree this proc is running, or empty when
	// nothing scheduled it. It is for log lines.
	NodeID() string

	// ResumeToken is what the previous attempt at this leaf reported when it
	// was stopped, and is nil on a leaf's first attempt.
	ResumeToken() []byte
}

var (
	_ Scorer  = (*Ctx)(nil)
	_ Stopper = (*Ctx)(nil)
	_ Exiter  = (*Ctx)(nil)
	_ Runtime = (*Ctx)(nil)
)
