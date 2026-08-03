// Package clnt is what an application uses to run work under the value-proc
// scheduler.
//
// It is the only part of the layer an application touches. Nothing here
// exposes how scheduling decisions are made, and an application that imports
// policy, gate or adapter is doing something wrong.
package clnt

import (
	"errors"
	"fmt"
	"iter"
	"time"

	protobuf "google.golang.org/protobuf/proto"

	"sigmaos/proc"
	rpcclntcache "sigmaos/rpc/clnt/cache"
	sprpcclnt "sigmaos/rpc/clnt/sigmap"
	"sigmaos/sigmaclnt/fslib"
	"sigmaos/valueprocs"
	"sigmaos/valueprocs/proto"
)

// ErrStaleEpoch means the service was restarted and lost every tree it was
// tracking. A cursor held across it is meaningless, and so is everything the
// caller believes about the tree's progress.
var ErrStaleEpoch = errors.New("valueprocs: the scheduler restarted; resubmit")

// --- describing work -------------------------------------------------------

// WorkNode is one node of a tree of work, as an application describes it.
//
// It is a description and nothing more: the scheduler keeps its own node once
// a tree is submitted, and the two are related only by position in the tree.
type WorkNode struct{ spec *proto.NodeSpec }

// Leaf wraps one proc as a unit of work.
//
// The proc must be idempotent: the scheduler may stop it to reclaim capacity
// and run it again from scratch, so anything it does must be safe to do
// twice. That cannot be checked, and is a promise the application makes.
func Leaf(p *proc.Proc) *WorkNode {
	return &WorkNode{spec: &proto.NodeSpec{LeafProc: p.GetProto()}}
}

// Select builds a node satisfied once k of its children are.
//
// The k-to-n gap is the whole point: it is slack the scheduler may spend when
// the cluster is idle and reclaim when it is busy, and how it spends it
// depends on what the children report about themselves.
//
//	Select(1, w1, ..., w15)                     // keep the best trial
//	Select(6, w1, ..., w9)                      // any six of nine will do
//	Select(n, t1, t2, Select(1, t3, t3Dup), t4) // t3 may be raced
//
// Children may themselves be Selects, which is how work that has no slack at
// the top gets some further down.
func Select(k int, children ...*WorkNode) (*WorkNode, error) {
	if len(children) == 0 {
		return nil, fmt.Errorf("valueprocs: Select with no children")
	}
	if k < 1 || k > len(children) {
		return nil, fmt.Errorf("valueprocs: Select k=%d with %d children", k, len(children))
	}
	cs := make([]*proto.NodeSpec, 0, len(children))
	for _, c := range children {
		if c == nil {
			return nil, fmt.Errorf("valueprocs: Select with a nil child")
		}
		cs = append(cs, c.spec)
	}
	return &WorkNode{spec: &proto.NodeSpec{K: int32(k), Children: cs}}, nil
}

// WithLabel names a node.
//
// A node's identity is derived from position in the tree, which no
// application wants to read. A label is how a report about "r.1.0" becomes a
// report about "trial-7".
func (n *WorkNode) WithLabel(s string) *WorkNode {
	n.spec.Label = s
	return n
}

// --- the client ------------------------------------------------------------

type Clnt struct {
	rpcc *rpcclntcache.ClntCache
}

func NewClnt(fsl *fslib.FsLib) *Clnt {
	return &Clnt{rpcc: rpcclntcache.NewRPCClntCache(sprpcclnt.WithSPChannel(fsl, false))}
}

// call retries while the service is merely absent, because it may not have
// registered yet or may be being restarted underneath us, and fails on
// anything else.
func (c *Clnt) call(method string, req, rep protobuf.Message) error {
	return c.rpcc.RPCRetryNotFound(valueprocs.VALUESCHED, valueprocs.VALUESCHEDREL, method, req, rep)
}

// Submit registers a tree and returns once the scheduler has accepted it, not
// once the work is done.
//
// tid is the caller's to choose, and submitting one twice is an error rather
// than a second tree. That is deliberate: it makes a submission safe to retry
// over a connection that may have dropped after the service already acted.
func (c *Clnt) Submit(tid, label string, root *WorkNode) error {
	if root == nil {
		return fmt.Errorf("valueprocs: Submit with no root")
	}
	req := &proto.SubmitTreeReq{TID: tid, Label: label, Root: root.spec}
	rep := &proto.SubmitTreeRep{}
	return c.call("Srv.SubmitTree", req, rep)
}

// Cancel stops everything a tree is running.
func (c *Clnt) Cancel(tid string) error {
	return c.call("Srv.CancelTree", &proto.CancelTreeReq{TID: tid}, &proto.CancelTreeRep{})
}

// Status samples a tree. It never blocks and carries no results.
func (c *Clnt) Status(tid string) (*proto.TreeStatusRep, error) {
	rep := &proto.TreeStatusRep{}
	if err := c.call("Srv.TreeStatus", &proto.TreeStatusReq{TID: tid}, rep); err != nil {
		return nil, err
	}
	return rep, nil
}

// SchedStats explains what the scheduler has been doing.
func (c *Clnt) SchedStats() (*proto.SchedStatsRep, error) {
	rep := &proto.SchedStatsRep{}
	if err := c.call("Srv.SchedStats", &proto.SchedStatsReq{}, rep); err != nil {
		return nil, err
	}
	return rep, nil
}

// --- collecting results ----------------------------------------------------

// Cursor is a place in a tree's results.
//
// It is a pair because a position counts within one numbering, and the
// numbering restarts when the service does. The zero value asks from the
// beginning and asserts nothing about which incarnation it came from.
type Cursor struct {
	Epoch uint64
	Since uint64
}

// Result is one leaf's finished work.
type Result struct {
	NodeID string
	Label  string
	Run    uint64
	Seq    uint64

	// Status is what the proc exited with, rehydrated whole -- so Data() and
	// mapstructure.Decode work exactly as they would have if the application
	// had spawned the proc itself, which it cannot, because the scheduler is
	// the parent.
	Status *proc.Status
}

// Batch is one read's worth of results.
type Batch struct {
	Results []Result
	Next    Cursor
	Done    bool
	State   string
}

// Next returns everything a tree produced after cur.
//
// Reading consumes nothing, so the same cursor twice gives the same answer
// and a call that fails halfway may simply be made again. A positive wait
// parks until something lands, the tree finishes, or the wait elapses.
func (c *Clnt) Next(tid string, cur Cursor, wait time.Duration) (*Batch, error) {
	req := &proto.NextResultsReq{
		TID:    tid,
		Since:  cur.Since,
		Epoch:  cur.Epoch,
		WaitMs: wait.Milliseconds(),
	}
	rep := &proto.NextResultsRep{}
	if err := c.call("Srv.NextResults", req, rep); err != nil {
		return nil, err
	}
	if rep.StaleEpoch {
		return nil, ErrStaleEpoch
	}

	b := &Batch{
		Results: make([]Result, 0, len(rep.Results)),
		Next:    Cursor{Epoch: rep.Epoch, Since: rep.Next},
		Done:    rep.TreeDone,
		State:   rep.TreeState,
	}
	for _, r := range rep.Results {
		b.Results = append(b.Results, Result{
			NodeID: r.GetRef().GetNodeID(),
			Label:  r.Label,
			Run:    r.GetRef().GetRun(),
			Seq:    r.Seq,
			Status: proc.NewStatusFromBytes(r.ProcStatus),
		})
	}
	return b, nil
}

// DefaultWait is how long one read parks before coming back empty-handed.
// Shorter than the service's own ceiling, so the client is what gives up.
const DefaultWait = 20 * time.Second

// Results yields each leaf's output as it lands.
//
// This is the incremental form, and it is the one worth using when the work
// downstream can start on a result without waiting for its siblings.
func (c *Clnt) Results(tid string) iter.Seq2[Result, error] {
	return func(yield func(Result, error) bool) {
		cur := Cursor{}
		for {
			b, err := c.Next(tid, cur, DefaultWait)
			if err != nil {
				yield(Result{}, err)
				return
			}
			for _, r := range b.Results {
				if !yield(r, nil) {
					return
				}
			}
			// Advanced only once the batch has been handed over, so a caller
			// that stops halfway re-reads rather than skips.
			cur = b.Next
			if b.Done {
				return
			}
		}
	}
}

// Wait blocks until a tree settles and returns every result, keyed by node.
//
// Note what it does not return: an entry per leaf. A Select(6, 9) yields six
// results and three silences, because only work that succeeded produces
// anything. Counting to nine would wait forever, which is why the tree's
// final state comes back alongside.
func (c *Clnt) Wait(tid string) (map[string]Result, string, error) {
	out := map[string]Result{}
	cur := Cursor{}
	for {
		b, err := c.Next(tid, cur, DefaultWait)
		if err != nil {
			return nil, "", err
		}
		for _, r := range b.Results {
			out[r.NodeID] = r
		}
		cur = b.Next
		if b.Done {
			return out, b.State, nil
		}
	}
}
