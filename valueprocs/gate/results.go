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

// Cursor is a reader's place in a tree's log.
//
// It is a pair and not a number on purpose. A position counts within one
// numbering, and the numbering restarts from nothing when the gate's state
// does -- so a bare position from a previous incarnation is not stale data
// but a different meaning wearing the same digits, and answering it would
// hand back an unrelated result the reader believes it has already passed.
// Carrying the epoch alongside is what makes that detectable instead of
// silent.
type Cursor struct {
	// Epoch is zero on a first read, which asserts nothing and is always
	// accepted. Anything else is checked.
	Epoch uint64
	Since uint64
}

// Batch is the answer to one read: everything after the position the caller
// asked from, plus where that leaves them.
type Batch struct {
	// Epoch identifies this incarnation of the gate's state.
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

// Cursor is where this batch leaves the reader.
func (b Batch) Cursor() Cursor { return Cursor{Epoch: b.Epoch, Since: b.Next} }

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

// Results returns everything a tree produced after the caller's cursor.
//
// The caller holds the position and the gate holds the log, so this keeps no
// per-reader state: nothing to register, nothing to clean up when a reader
// disappears, and any number of readers may sit at different positions
// without affecting each other. The same cursor twice gives the same answer,
// so a call that fails halfway can simply be made again.
//
// The epoch is checked here rather than by whoever accepts reads, because a
// transport that forgot to check would not fail -- it would quietly serve one
// incarnation's results against another's numbering, which is the exact harm
// the epoch exists to prevent. An invariant that fails silently belongs where
// it cannot be skipped.
//
// A positive wait parks until something lands, the tree finishes, or the wait
// elapses, whichever comes first. It is wall-clock and deliberately not the
// injected clock: that clock exists to make policy deterministic under test,
// whereas this bounds a caller that is genuinely waiting. A wait of zero
// returns whatever is available immediately.
func (g *Gate) Results(id policy.TreeID, cur Cursor, wait time.Duration) (Batch, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if cur.Epoch != 0 && cur.Epoch != g.epoch {
		return Batch{}, ErrStaleEpoch
	}
	since := cur.Since

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
