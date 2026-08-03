package adapter

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"sync"
	"time"

	"sigmaos/api/fs"
	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/valueprocs"
	"sigmaos/valueprocs/gate"
	"sigmaos/valueprocs/policy"
	"sigmaos/valueprocs/proto"
)

// MaxWait bounds how long a read may park in the service.
//
// A caller asking to wait indefinitely would outlive the session underneath
// it, and the call would fail in a way indistinguishable from the service
// having died. Returning empty-handed instead lets the caller ask again with
// the same position, which costs a round trip and nothing else.
const MaxWait = 30 * time.Second

// ErrTreeIDInUse means a tID already names a different tree. It is not a
// retry, so accepting it would silently hand the caller somebody else's work.
var ErrTreeIDInUse = errors.New("valueprocs: tree id already names another tree")

// Srv is the RPC surface. It translates and nothing else: every judgment
// lives in policy, and everything that touches SigmaOS lives in the rest of
// this package.
type Srv struct {
	gate   *gate.Gate
	exec   *Exec
	probe  *Probe
	period time.Duration

	// mu guards submitted, which fingerprints what each tID was registered
	// with. It is separate from the gate's lock and never held across it in
	// the other direction, because it protects a different question: not what
	// the scheduler should do, but whether this request has been asked before.
	mu        sync.Mutex
	submitted map[policy.TreeID]string
}

// --- demand ----------------------------------------------------------------

// SubmitTree registers a tree and starts whatever its quorum requires.
//
// Sending the same tree under the same tID twice registers one tree and
// reports the repeat, because a caller whose connection dropped cannot tell
// whether the service acted, and its only safe move is to ask again.
func (s *Srv) SubmitTree(ctx fs.CtxI, req proto.SubmitTreeReq, rep *proto.SubmitTreeRep) error {
	// Validated before anything else, so that a tree that cannot run is
	// reported as such rather than as a collision with one that can.
	root, err := GroupFromProto(req.Root)
	if err != nil {
		db.DPrintf(db.VALUEPROC_ERR, "SubmitTree %v: %v", req.TID, err)
		return err
	}
	key := submitKey(ctx, req.Label, req.Root)

	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, dup := s.submitted[policy.TreeID(req.TID)]; dup {
		if prev != key {
			db.DPrintf(db.VALUEPROC_ERR, "SubmitTree %v: %v", req.TID, ErrTreeIDInUse)
			return ErrTreeIDInUse
		}
		db.DPrintf(db.VALUEPROC, "SubmitTree %v: already registered", req.TID)
		rep.TID, rep.Created = req.TID, false
		return nil
	}

	id, err := s.gate.Submit(policy.TreeSpec{
		ID:        policy.TreeID(req.TID),
		Label:     req.Label,
		Root:      root,
		Attrs:     attrsOf(ctx),
		Submitted: time.Now(),
	})
	if err != nil {
		db.DPrintf(db.VALUEPROC_ERR, "SubmitTree %v: %v", req.TID, err)
		return err
	}
	s.submitted[id] = key
	db.DPrintf(db.VALUEPROC, "SubmitTree %v by %v", id, ctx.Principal())
	rep.TID, rep.Created = string(id), true
	return nil
}

// submitKey fingerprints what a submission asks for, so that a retry can be
// told apart from two callers reaching for the same name.
//
// It is taken over the tree as this layer understands it -- its shape and
// labels, and each leaf's program, arguments, environment and resources --
// rather than over the request's bytes. A submitted proc arrives carrying a
// pid that Build throws away and mints fresh for every attempt, so hashing the
// message would make two descriptions of identical work look different on the
// strength of a field the layer has already decided means nothing.
func submitKey(ctx fs.CtxI, label string, root *proto.NodeSpec) string {
	h := sha256.New()
	if p := ctx.Principal(); p != nil {
		io.WriteString(h, p.GetID().String())
	}
	io.WriteString(h, "\x00"+label)
	hashSpec(h, root)
	return string(h.Sum(nil))
}

// hashSpec writes a tree into h. Nesting is bracketed so that no two different
// shapes can flatten to the same bytes.
func hashSpec(h io.Writer, n *proto.NodeSpec) {
	if n == nil {
		io.WriteString(h, "()")
		return
	}
	fmt.Fprintf(h, "(%q,%d", n.Label, n.K)
	if len(n.Children) == 0 {
		// Read through the template, which is what makes this a fingerprint of
		// the work rather than of the message: everything Build regenerates is
		// absent from it by construction.
		//
		// A leaf that has no runnable proc is written as whatever went wrong
		// rather than skipped, so that the brackets balance either way. The
		// caller validates first, so it never gets here.
		switch t, err := templateOf(n.LeafProc); {
		case err != nil:
			fmt.Fprintf(h, ",unrunnable %v", err)
		default:
			fmt.Fprintf(h, ",%q,%q,%d,%d,%d", t.program, t.args, t.typ, t.mcpu, t.mem)
			for _, k := range slices.Sorted(maps.Keys(t.env)) {
				// The one key that varies between two constructions of the
				// same proc, and the one Build overwrites with the new pid.
				if k == proc.SIGMADEBUGPID {
					continue
				}
				fmt.Fprintf(h, ",%q=%q", k, t.env[k])
			}
		}
	}
	for _, c := range n.Children {
		hashSpec(h, c)
	}
	io.WriteString(h, ")")
}

// attrsOf describes the submitter, for an arbiter to divide capacity on.
//
// Read off the request, never out of it. An application asked to rank its own
// importance always answers "high", so the only trustworthy source is who it
// turned out to be rather than what it claimed.
func attrsOf(ctx fs.CtxI) map[string]string {
	p := ctx.Principal()
	if p == nil {
		return nil
	}
	return map[string]string{
		valueprocs.ATTR_REALM:     p.GetRealm().String(),
		valueprocs.ATTR_PRINCIPAL: p.GetID().String(),
	}
}

// CancelTree stops everything a tree is running.
func (s *Srv) CancelTree(ctx fs.CtxI, req proto.CancelTreeReq, rep *proto.CancelTreeRep) error {
	if err := s.gate.Cancel(policy.TreeID(req.TID)); err != nil {
		db.DPrintf(db.VALUEPROC_ERR, "CancelTree %v: %v", req.TID, err)
		return err
	}
	db.DPrintf(db.VALUEPROC, "CancelTree %v", req.TID)
	rep.OK = true
	return nil
}

// --- value -----------------------------------------------------------------

// ReportScore records what a running attempt says about itself. This is the
// hot path: one call per attempt per reporting interval, and nothing else in
// the system polls anybody.
func (s *Srv) ReportScore(ctx fs.CtxI, req proto.ReportScoreReq, rep *proto.ReportScoreRep) error {
	s.gate.OnScore(refOf(req.Ref), policy.Score(req.Score), policy.Gradient(req.Gradient))
	rep.OK = true
	return nil
}

func refOf(r *proto.RunRef) policy.RunRef {
	if r == nil {
		return policy.RunRef{}
	}
	return policy.RunRef{
		Tree: policy.TreeID(r.TID),
		Node: policy.NodeID(r.NodeID),
		Run:  policy.RunID(r.Run),
	}
}

func refProto(r policy.RunRef) *proto.RunRef {
	return &proto.RunRef{TID: string(r.Tree), NodeID: string(r.Node), Run: uint64(r.Run)}
}

// --- results ---------------------------------------------------------------

// NextResults returns everything a tree produced after the caller's position.
func (s *Srv) NextResults(ctx fs.CtxI, req proto.NextResultsReq, rep *proto.NextResultsRep) error {
	wait := min(time.Duration(req.WaitMs)*time.Millisecond, MaxWait)

	// The epoch travels with the position and is checked inside Results, so
	// there is no way for this handler to serve one incarnation's results
	// against another's numbering by forgetting to look.
	cur := gate.Cursor{Epoch: req.Epoch, Since: req.Since}
	b, err := s.gate.Results(policy.TreeID(req.TID), cur, wait)
	if errors.Is(err, gate.ErrStaleEpoch) {
		// Answered rather than refused. The caller has to act on this -- throw
		// away its position and start over -- and an error's identity does not
		// survive an RPC, so it would arrive as text to be matched.
		db.DPrintf(db.VALUEPROC_ERR, "NextResults %v: %v is not epoch %v",
			req.TID, req.Epoch, s.gate.Epoch())
		rep.StaleEpoch = true
		rep.Epoch = s.gate.Epoch()
		return nil
	}
	if err != nil {
		db.DPrintf(db.VALUEPROC_ERR, "NextResults %v from %v: %v", req.TID, cur, err)
		return err
	}

	rep.Results = make([]*proto.LeafResult, 0, len(b.Results))
	for _, r := range b.Results {
		rep.Results = append(rep.Results, &proto.LeafResult{
			Seq:        r.Seq,
			Ref:        refProto(r.Ref),
			Label:      r.Label,
			ProcStatus: r.Data,
		})
	}
	rep.Next = b.Next
	rep.Epoch = b.Epoch
	rep.TreeDone = b.Done
	rep.TreeState = b.State.String()
	return nil
}

// --- reporting -------------------------------------------------------------

// TreeStatus samples a tree. It never blocks and carries no results, which is
// what makes it cheap enough to poll: results are an event stream, collected
// with NextResults, and status is a sample where the latest wins.
func (s *Srv) TreeStatus(ctx fs.CtxI, req proto.TreeStatusReq, rep *proto.TreeStatusRep) error {
	v, ok := s.gate.Status(policy.TreeID(req.TID))
	if !ok {
		return gate.ErrUnknownTree
	}
	rep.TID = string(v.ID)
	rep.Label = v.Label
	rep.State = v.State.String()
	rep.Cancelled = v.Cancelled
	rep.SubmittedUnixNs = v.Submitted.UnixNano()
	rep.NRunning = int32(v.NRunning)
	rep.NCharged = int32(v.NCharged)
	rep.Nodes = make([]*proto.NodeStatus, 0, len(v.Nodes))
	for _, n := range v.Nodes {
		rep.Nodes = append(rep.Nodes, s.nodeStatus(v.ID, n))
	}
	return nil
}

func (s *Srv) nodeStatus(t policy.TreeID, n policy.NodeView) *proto.NodeStatus {
	ns := &proto.NodeStatus{
		NodeID:    string(n.ID),
		Label:     n.Label,
		State:     n.State.String(),
		K:         int32(n.K),
		NChildren: int32(n.NChildren),
		NRunning:  int32(n.NRunning),
		NCharged:  int32(n.NCharged),
		Target:    int32(n.Target),
		IsLeaf:    n.IsLeaf,
	}
	if !n.IsLeaf {
		return ns
	}
	ns.Workload = n.Workload
	ns.Run = uint64(n.Run)
	ns.RunState = n.RunState.String()
	ns.Attempts = int32(n.Attempts)
	ns.Stops = int32(n.Stops)
	ns.Score = float64(n.Score)
	ns.Gradient = float64(n.Gradient)
	ns.HasScore = n.HasScore
	ns.ScoreStale = n.ScoreStale
	// The pid is the adapter's to supply: nothing above this package knows
	// one, and it is only in flight while the attempt is.
	if pid, ok := s.exec.PidOf(policy.RunRef{Tree: t, Node: n.ID, Run: n.Run}); ok {
		ns.PID = pid.String()
	}
	return ns
}

// SchedStats explains the layer's behaviour from outside.
func (s *Srv) SchedStats(ctx fs.CtxI, req proto.SchedStatsReq, rep *proto.SchedStatsRep) error {
	st := s.gate.Stats()
	rep.Pressure = st.Pressure
	rep.Busy = st.Busy
	rep.DelayPressure = st.DelayPressure
	rep.Components = st.Components
	rep.NTrees = int32(st.NTrees)
	rep.NRunning = int32(st.NRunning)
	rep.NCharged = int32(st.NCharged)
	rep.Slots = int32(st.Slots)
	rep.NScoreDropped = int32(st.NScoreDropped)

	// Counted by kind rather than totalled: a flat count cannot answer
	// "mostly pruning or mostly speculating?", which is the question the
	// reasons exist to answer.
	rep.NStartsByKind = make(map[uint32]int32, len(st.NStarts))
	for k, n := range st.NStarts {
		rep.NStartsByKind[uint32(k)] = int32(n)
	}
	rep.NStopsByKind = make(map[uint32]int32, len(st.NStops))
	for k, n := range st.NStops {
		rep.NStopsByKind[uint32(k)] = int32(n)
	}

	es := s.exec.Stats()
	rep.NSynthesizedStopped = es.NSynthesizedStopped
	rep.NSynthesizedFailed = es.NSynthesizedFailed
	rep.NEvictRetries = es.NEvictRetries
	rep.NUnrequestedEvicts = es.NUnrequestedEvicts

	rep.Epoch = s.gate.Epoch()
	return nil
}
