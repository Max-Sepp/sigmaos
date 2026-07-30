package adapter

import (
	"time"

	"sigmaos/api/fs"
	db "sigmaos/debug"
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

// Srv is the RPC surface. It translates and nothing else: every judgment
// lives in policy, and everything that touches SigmaOS lives in the rest of
// this package.
type Srv struct {
	gate   *gate.Gate
	exec   *Exec
	probe  *Probe
	period time.Duration
}

// --- demand ----------------------------------------------------------------

// SubmitTree registers a tree and starts whatever its quorum requires.
func (s *Srv) SubmitTree(ctx fs.CtxI, req proto.SubmitTreeReq, rep *proto.SubmitTreeRep) error {
	root, err := GroupFromProto(req.Root)
	if err != nil {
		db.DPrintf(db.VALUEPROC_ERR, "SubmitTree %v: %v", req.TID, err)
		return err
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
	db.DPrintf(db.VALUEPROC, "SubmitTree %v by %v", id, ctx.Principal())
	rep.TID = string(id)
	return nil
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
