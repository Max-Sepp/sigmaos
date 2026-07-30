package adapter

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"sigmaos/api/fs"
	"sigmaos/proc"
	sessp "sigmaos/session/proto"
	sp "sigmaos/sigmap"
	"sigmaos/sigmasrv/clntcond"
	"sigmaos/valueprocs"
	"sigmaos/valueprocs/gate"
	"sigmaos/valueprocs/policy"
	"sigmaos/valueprocs/proto"
)

// ctx is the identity a request arrived with. Only Principal is ever read;
// the rest of fs.CtxI exists for the filesystem protocol.
type ctx struct{ p *sp.Tprincipal }

func newCtx(id, realm string) *ctx {
	return &ctx{p: sp.NewPrincipal(sp.TprincipalID(id), sp.Trealm(realm))}
}

func (c *ctx) Principal() *sp.Tprincipal              { return c.p }
func (c *ctx) Secrets() map[string]*sp.SecretProto    { return nil }
func (c *ctx) SessionId() sessp.Tsession              { return 0 }
func (c *ctx) ClntCondTable() *clntcond.ClntCondTable { return nil }
func (c *ctx) ClntId() sp.TclntId                     { return 0 }
func (c *ctx) FenceFs() fs.Dir                        { return nil }

var _ fs.CtxI = (*ctx)(nil)

const testEpoch = 7

func newSrv(t *testing.T) (*Srv, *fakeProcAPI) {
	t.Helper()
	f := newFakeProcAPI()
	ex := NewExec(f, nil, testPolicy())
	sd := policy.NewScheduler(policy.DefaultConfig(), ex, FairShare{}, nil)
	g := gate.New(sd, gate.WithEpoch(testEpoch))
	ex.ev = g
	t.Cleanup(func() { g.Close(); ex.Close() })
	return &Srv{gate: g, exec: ex}, f
}

// submitSpec is a k-of-n tree of plain leaves.
func submitSpec(id string, k, n int) proto.SubmitTreeReq {
	cs := make([]*proto.NodeSpec, 0, n)
	for i := range n {
		cs = append(cs, leafSpec(string(rune('a'+i))))
	}
	return proto.SubmitTreeReq{TID: id, Label: "job", Root: &proto.NodeSpec{K: int32(k), Children: cs}}
}

func TestSubmitThenCollectResults(t *testing.T) {
	s, f := newSrv(t)

	rep := &proto.SubmitTreeRep{}
	assert.NoError(t, s.SubmitTree(newCtx("app", "r1"), submitSpec("t", 2, 2), rep))
	assert.Equal(t, "t", rep.TID)

	// Nothing has finished, so a non-blocking read is empty and not done.
	nr := &proto.NextResultsRep{}
	assert.NoError(t, s.NextResults(newCtx("app", "r1"), proto.NextResultsReq{TID: "t"}, nr))
	assert.Empty(t, nr.Results)
	assert.False(t, nr.TreeDone)
	assert.EqualValues(t, testEpoch, nr.Epoch)

	for i := range 2 {
		pid := f.pidOf(t, i)
		f.exit(pid, proc.NewStatusInfo(proc.StatusOK, "", i), nil)
	}
	eventually(t, func() bool {
		nr = &proto.NextResultsRep{}
		assert.NoError(t, s.NextResults(newCtx("app", "r1"), proto.NextResultsReq{TID: "t"}, nr))
		return nr.TreeDone
	}, "the tree finished")

	assert.Len(t, nr.Results, 2)
	assert.EqualValues(t, 2, nr.Next)
	assert.Equal(t, "satisfied", nr.TreeState)
	for i, r := range nr.Results {
		assert.EqualValues(t, i+1, r.Seq)
		assert.Equal(t, "t", r.Ref.TID)
		// The whole status travels, so the submitter reads Data() exactly as
		// it would have had it spawned the proc itself.
		assert.True(t, proc.NewStatusFromBytes(r.ProcStatus).IsStatusOK())
	}
}

func TestAttributesComeFromTheCallerNotTheRequest(t *testing.T) {
	s, _ := newSrv(t)

	// An application asked to rank its own importance always answers "high",
	// so the only trustworthy source is who it turned out to be.
	assert.NoError(t, s.SubmitTree(newCtx("hpsearch-7", "tenant-a"),
		submitSpec("t", 1, 1), &proto.SubmitTreeRep{}))

	v, ok := s.gate.Status("t")
	assert.True(t, ok)
	assert.Equal(t, "tenant-a", v.Attrs[valueprocs.ATTR_REALM])
	assert.Equal(t, "hpsearch-7", v.Attrs[valueprocs.ATTR_PRINCIPAL])
}

func TestSubmitRefusesATreeThatCannotRun(t *testing.T) {
	s, f := newSrv(t)

	err := s.SubmitTree(newCtx("app", "r1"),
		proto.SubmitTreeReq{TID: "t", Root: &proto.NodeSpec{K: 3, Children: []*proto.NodeSpec{leafSpec("a")}}},
		&proto.SubmitTreeRep{})
	assert.Error(t, err, "k above the number of children")
	assert.Equal(t, 0, f.nSpawned(), "nothing was started")

	// A duplicate is an error rather than a second tree, which is what makes
	// a submission safe to retry after a connection drops.
	assert.NoError(t, s.SubmitTree(newCtx("app", "r1"), submitSpec("t", 1, 1), &proto.SubmitTreeRep{}))
	assert.Error(t, s.SubmitTree(newCtx("app", "r1"), submitSpec("t", 1, 1), &proto.SubmitTreeRep{}))
}

func TestNextResultsRefusesAPositionFromAnotherIncarnation(t *testing.T) {
	s, _ := newSrv(t)
	assert.NoError(t, s.SubmitTree(newCtx("app", "r1"), submitSpec("t", 1, 1), &proto.SubmitTreeRep{}))

	err := s.NextResults(newCtx("app", "r1"),
		proto.NextResultsReq{TID: "t", Since: 3, Epoch: testEpoch + 1}, &proto.NextResultsRep{})
	assert.ErrorIs(t, err, gate.ErrStaleEpoch)

	// Zero asserts nothing, which is what a first read sends.
	assert.NoError(t, s.NextResults(newCtx("app", "r1"),
		proto.NextResultsReq{TID: "t"}, &proto.NextResultsRep{}))
}

func TestNextResultsWaitIsBounded(t *testing.T) {
	s, _ := newSrv(t)
	assert.NoError(t, s.SubmitTree(newCtx("app", "r1"), submitSpec("t", 1, 1), &proto.SubmitTreeRep{}))

	// A caller asking to wait forever would outlive the session underneath
	// it, and the failure would be indistinguishable from the service dying.
	nr := &proto.NextResultsRep{}
	assert.NoError(t, s.NextResults(newCtx("app", "r1"),
		proto.NextResultsReq{TID: "t", WaitMs: 20}, nr))
	assert.Empty(t, nr.Results)
}

func TestTreeStatusReportsTheTreeAndItsProcs(t *testing.T) {
	s, f := newSrv(t)
	assert.NoError(t, s.SubmitTree(newCtx("app", "r1"), submitSpec("t", 1, 2), &proto.SubmitTreeRep{}))
	f.pidOf(t, 0)

	rep := &proto.TreeStatusRep{}
	assert.NoError(t, s.TreeStatus(newCtx("app", "r1"), proto.TreeStatusReq{TID: "t"}, rep))
	assert.Equal(t, "job", rep.Label)
	assert.Equal(t, "pending", rep.State)
	assert.Len(t, rep.Nodes, 3, "a root and two leaves")

	root := rep.Nodes[0]
	assert.False(t, root.IsLeaf)
	assert.EqualValues(t, 1, root.K)
	assert.EqualValues(t, 2, root.NChildren)

	// A label is how a client tells fifteen interchangeable leaves apart,
	// since node identity is derived from tree position rather than given.
	leaf := rep.Nodes[1]
	assert.True(t, leaf.IsLeaf)
	assert.Equal(t, "a", leaf.Label)
	assert.Equal(t, "sleeper", leaf.Workload)

	// The pid is the handle an operator needs to go and look at something,
	// and nothing above the adapter knows one.
	assert.Equal(t, f.procForNode(t, "r.0").GetPid().String(), leaf.PID)

	assert.Error(t, s.TreeStatus(newCtx("app", "r1"), proto.TreeStatusReq{TID: "nope"}, rep))
}

func TestSchedStatsBreaksCountsDownByKind(t *testing.T) {
	s, f := newSrv(t)
	assert.NoError(t, s.SubmitTree(newCtx("app", "r1"), submitSpec("t", 1, 3), &proto.SubmitTreeRep{}))
	for i := range 3 {
		f.pidOf(t, i)
	}
	// One wins, and the other two are stopped because the quorum is met.
	f.exit(f.pidOf(t, 0), proc.NewStatus(proc.StatusOK), nil)
	eventually(t, func() bool { return f.nEvicted() == 2 }, "the losers were stopped")

	rep := &proto.SchedStatsRep{}
	assert.NoError(t, s.SchedStats(newCtx("app", "r1"), proto.SchedStatsReq{}, rep))
	assert.EqualValues(t, 1, rep.NTrees)
	assert.EqualValues(t, testEpoch, rep.Epoch)

	// A flat total could not answer "mostly pruning or mostly speculating?",
	// which is the question the reasons exist for. Here all three attempts
	// started, but only one of them was needed: an idle cluster is why the
	// other two ran, and they are the ones a busy cluster would not have.
	assert.EqualValues(t, 1, rep.NStartsByKind[uint32(policy.StartRequiredForQuorum)])
	assert.EqualValues(t, 2, rep.NStartsByKind[uint32(policy.StartSlackRedundancy)])
	assert.EqualValues(t, 2, rep.NStopsByKind[uint32(policy.StopQuorumReached)])
}

func TestReportScoreAndCancel(t *testing.T) {
	s, f := newSrv(t)
	assert.NoError(t, s.SubmitTree(newCtx("app", "r1"), submitSpec("t", 1, 2), &proto.SubmitTreeRep{}))
	f.pidOf(t, 0)

	ref := &proto.RunRef{TID: "t", NodeID: "r.0", Run: 0}
	s.gate.OnRunStarted(refOf(ref))

	sr := &proto.ReportScoreRep{}
	assert.NoError(t, s.ReportScore(newCtx("app", "r1"),
		proto.ReportScoreReq{Ref: ref, Score: 0.75, Gradient: 0.2}, sr))
	assert.True(t, sr.OK)

	ts := &proto.TreeStatusRep{}
	assert.NoError(t, s.TreeStatus(newCtx("app", "r1"), proto.TreeStatusReq{TID: "t"}, ts))
	assert.Equal(t, 0.75, ts.Nodes[1].Score)
	assert.True(t, ts.Nodes[1].HasScore)

	cr := &proto.CancelTreeRep{}
	assert.NoError(t, s.CancelTree(newCtx("app", "r1"), proto.CancelTreeReq{TID: "t"}, cr))
	assert.True(t, cr.OK)
	assert.Error(t, s.CancelTree(newCtx("app", "r1"), proto.CancelTreeReq{TID: "nope"}, &proto.CancelTreeRep{}))
}
