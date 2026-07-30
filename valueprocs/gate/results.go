package gate

import (
	"slices"
	"time"

	"sigmaos/valueprocs/policy"
)

// LeafResult is one leaf's finished output, as one entry in a tree's result
// log.
type LeafResult struct {
	// Seq is this entry's position in its tree's log, starting at 1. Entries
	// are appended in the order they happened and are never edited, removed
	// or reordered, which is what lets a reader describe everything it has
	// seen with a single number.
	Seq uint64

	Ref   policy.RunRef
	Label string

	// Data is what the attempt reported. It is opaque here.
	Data []byte
}

// Batch is the answer to one read: everything after the position the caller
// asked from, plus where that leaves them.
type Batch struct {
	// Epoch identifies this incarnation of the gate's state. A reader stores
	// it beside Next and sends both back; whoever accepts reads is
	// responsible for rejecting a position from a different epoch, since the
	// numbering restarts from nothing when the state does.
	Epoch uint64

	Results []LeafResult

	// Next is the position to ask from next time.
	Next uint64

	// Done reports that no entry will ever follow Next. It travels in the
	// same reply as the final results rather than being asked for
	// separately, so that a reader cannot learn a tree has finished before
	// it has been handed everything the tree produced.
	Done  bool
	State policy.NodeState
}

// results returns a tree's log, creating it on first use.
func (g *Gate) resultsL(id policy.TreeID) []LeafResult { return g.logs[id] }

// appendResultL records a completed attempt. Called with the lock held, from
// inside the same step that told the scheduler, so a reader woken by that
// step sees the entry and the tree's new state together.
func (g *Gate) appendResultL(s *policy.Scheduler, ref policy.RunRef, data []byte) {
	label := ""
	if v, ok := s.NodeView(ref.Tree, ref.Node); ok {
		label = v.Label
	}
	lg := g.logs[ref.Tree]
	g.logs[ref.Tree] = append(lg, LeafResult{
		Seq:   uint64(len(lg)) + 1,
		Ref:   ref,
		Label: label,
		Data:  data,
	})
}

// Results returns everything a tree produced after position since.
//
// The caller holds the position and the gate holds the log, so this keeps no
// per-reader state: nothing to register, nothing to clean up when a reader
// disappears, and any number of readers may sit at different positions
// without affecting each other. The same since twice gives the same answer,
// so a call that fails halfway can simply be made again.
//
// A positive wait parks until something lands, the tree finishes, or the wait
// elapses, whichever comes first. It is wall-clock and deliberately not the
// injected clock: that clock exists to make policy deterministic under test,
// whereas this bounds a caller that is genuinely waiting. A wait of zero
// returns whatever is available immediately.
func (g *Gate) Results(id policy.TreeID, since uint64, wait time.Duration) (Batch, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	expired := wait <= 0
	if !expired {
		t := time.AfterFunc(wait, func() {
			g.mu.Lock()
			expired = true
			g.cond.Broadcast()
			g.mu.Unlock()
		})
		defer t.Stop()
	}

	for {
		v, ok := g.sched.TreeView(id)
		if !ok {
			return Batch{}, ErrUnknownTree
		}
		lg := g.resultsL(id)
		done := v.State != policy.NodePending || v.Cancelled

		if uint64(len(lg)) > since || done || expired {
			// A position past the end is a reader that has lost track rather
			// than one that is caught up, but the honest answer to both is
			// the same: nothing new. Whoever checks epochs catches the
			// difference.
			from := min(since, uint64(len(lg)))
			return Batch{
				Epoch:   g.epoch,
				Results: slices.Clone(lg[from:]),
				Next:    uint64(len(lg)),
				Done:    done,
				State:   v.State,
			}, nil
		}
		if g.closed {
			return Batch{}, ErrClosed
		}
		g.cond.Wait()
	}
}

// Epoch identifies this incarnation of the gate's state.
func (g *Gate) Epoch() uint64 { return g.epoch }
