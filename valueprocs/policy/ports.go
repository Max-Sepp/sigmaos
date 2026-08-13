package policy

// RunStartStopper launches and ends attempts. It is the only interface this
// package calls.
//
// Both methods are invoked with no lock held, in sequence, from whichever
// goroutine ran the Effect, so an implementation that needs to wait should do
// so on a goroutine of its own.
//
// Exactly one terminal event must follow every call, eventually, always. That
// obligation is total: when the platform will not deliver one, the
// implementation is responsible for synthesizing it, because a slot stays
// charged until it arrives.
type RunStartStopper interface {
	// Start launches one attempt.
	Start(ref RunRef, l Launch, why StartReason)

	// Stop asks an attempt to end. Returning does not free the slot; only the
	// terminal event does.
	Stop(ref RunRef, why StopReason)
}

// Effect performs the platform calls a decision implies. Entry points return
// one instead of calling RunStartStopper inline, so a caller holding a lock
// can release it before anything blocks. It is nil when nothing is to be
// done, and it must be run at most once.
//
// An Effect closes over values captured when the decision was made, so it may
// safely run after the scheduler has moved on.
type Effect func()

// TreeShare is one tree's slot allowance.
type TreeShare struct {
	Tree  TreeID
	Slots int
}

// Arbiter divides capacity between trees competing for it. It is called
// inline while reconciling and must be pure. A nil Arbiter means no per-tree
// limit.
type Arbiter interface {
	Arbitrate(trees []TreeView, c Capacity) []TreeShare
}

// Logf receives one line per decision. It is supplied rather than imported so
// that this package depends on nothing to explain itself.
type Logf func(format string, v ...any)

// effects folds a batch of calls into a single Effect, preserving the order
// they were decided in.
func effects(fs []func()) Effect {
	if len(fs) == 0 {
		return nil
	}
	return func() {
		for _, f := range fs {
			f()
		}
	}
}
