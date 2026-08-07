package clnt

import (
	"iter"
	"time"

	"sigmaos/valueprocs/proto"
)

// Runner is enough to run a tree of work: hand it over, wait for it, and ask
// how far along it is in the meantime.
type Runner interface {
	// Submit registers a tree and returns once the scheduler has accepted
	// it, not once the work is done. It is idempotent under tid.
	Submit(tid, label string, root *WorkNode) (created bool, err error)

	// Status samples a tree. It never blocks and carries no results.
	Status(tid string) (*proto.TreeStatusRep, error)

	// Wait blocks until a tree settles and returns every result it produced,
	// keyed by node, alongside the tree's final state.
	Wait(tid string) (map[string]Result, string, error)
}

// Observer watches without steering: it can sample a tree and ask the
// scheduler what it has been doing, and it can do nothing else.
//
// It is what a progress monitor or a benchmark's sampler holds, and the
// point of holding it is that such a thing cannot submit or cancel work by
// accident.
type Observer interface {
	// Status samples a tree. It never blocks and carries no results.
	Status(tid string) (*proto.TreeStatusRep, error)

	// SchedStats explains what the scheduler has been doing.
	SchedStats() (*proto.SchedStatsRep, error)
}

// Sched is the whole of what the scheduler offers an application.
//
// Beyond Runner and Observer it adds cancellation and the incremental read,
// which is the one worth using when the work downstream can start on a
// result without waiting for its siblings.
type Sched interface {
	Runner
	Observer

	// Cancel stops everything a tree is running.
	Cancel(tid string) error

	// Next returns everything a tree produced after cur, parking up to wait
	// for something to land.
	Next(tid string, cur Cursor, wait time.Duration) (*Batch, error)

	// Results yields each leaf's output as it lands.
	Results(tid string) iter.Seq2[Result, error]
}

var (
	_ Runner   = (*Clnt)(nil)
	_ Observer = (*Clnt)(nil)
	_ Sched    = (*Clnt)(nil)
)
