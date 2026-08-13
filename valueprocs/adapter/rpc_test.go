package adapter

import (
	"testing"
	"time"

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
	ex := NewExec(f, nil, testTuning())
	sd := policy.NewScheduler(policy.DefaultConfig(), ex, FairShare{}, nil)
	g := gate.New(sd, gate.WithEpoch(testEpoch))
	ex.ev = g
	t.Cleanup(func() { g.Close(); ex.Close() })
	return &Srv{gate: g, exec: ex, submitted: make(map[policy.TreeID]string)}, f
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

// TestResubmittingTheSameTreeIsARepeatNotASecondTree is what makes a
// submission retryable. A call that fails on the way back is indistinguishable
// from one that never arrived, so a caller's only safe move is to ask again --
// and the service has to be the one that knows it has already answered.
func TestResubmittingTheSameTreeIsARepeatNotASecondTree(t *testing.T) {
	s, f := newSrv(t)

	rep := &proto.SubmitTreeRep{}
	assert.NoError(t, s.SubmitTree(newCtx("app", "r1"), submitSpec("t", 2, 2), rep))
	assert.True(t, rep.Created)
	eventually(t, func() bool { return f.nSpawned() == 2 }, "the tree started")

	// Rebuilt rather than resent, which is what a caller that gave up waiting
	// and asked again would do. The pid inside a submitted proc differs every
	// time it is constructed and the layer regenerates it anyway, so it must
	// not be what decides whether this is the same tree.
	again := &proto.SubmitTreeRep{}
	assert.NoError(t, s.SubmitTree(newCtx("app", "r1"), submitSpec("t", 2, 2), again))
	assert.Equal(t, "t", again.TID)
	assert.False(t, again.Created, "the repeat was reported as a fresh registration")

	// The point of the whole exercise: the work is not started twice.
	consistently(t, 50*time.Millisecond, func() bool { return f.nSpawned() == 2 },
		"the repeat started the tree a second time")
}

// TestADifferentTreeUnderTheSameIDIsRefused draws the other line. Accepting it
// would hand the caller a tree it never described, under a name it thinks it
// owns, which is worse than an error either way it is read.
func TestADifferentTreeUnderTheSameIDIsRefused(t *testing.T) {
	s, _ := newSrv(t)

	rep := &proto.SubmitTreeRep{}
	assert.NoError(t, s.SubmitTree(newCtx("app", "r1"), submitSpec("t", 2, 2), rep))

	err := s.SubmitTree(newCtx("app", "r1"), submitSpec("t", 1, 3), &proto.SubmitTreeRep{})
	assert.ErrorIs(t, err, ErrTreeIDInUse)

	// Including when the tree is identical but somebody else is asking, since
	// a tree id means something only within one caller's namespace.
	err = s.SubmitTree(newCtx("other", "r1"), submitSpec("t", 2, 2), &proto.SubmitTreeRep{})
	assert.ErrorIs(t, err, ErrTreeIDInUse)
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

	// And the shape is judged before the name: a tree that cannot run is
	// reported as such rather than as a collision with the id it asked for.
	assert.NoError(t, s.SubmitTree(newCtx("app", "r1"), submitSpec("t", 1, 1), &proto.SubmitTreeRep{}))
	err = s.SubmitTree(newCtx("app", "r1"),
		proto.SubmitTreeReq{TID: "t", Root: &proto.NodeSpec{K: 3, Children: []*proto.NodeSpec{leafSpec("a")}}},
		&proto.SubmitTreeRep{})
	assert.Error(t, err)
	assert.NotErrorIs(t, err, ErrTreeIDInUse)
}

func TestNextResultsRefusesAPositionFromAnotherIncarnation(t *testing.T) {
	s, _ := newSrv(t)
	assert.NoError(t, s.SubmitTree(newCtx("app", "r1"), submitSpec("t", 1, 1), &proto.SubmitTreeRep{}))

	// Answered, not refused. The caller must act on this -- throw away its
	// position and start over -- and an error's identity does not survive an
	// RPC, so refusing would leave it matching text to decide whether its
	// whole view of the tree is void.
	rep := &proto.NextResultsRep{}
	assert.NoError(t, s.NextResults(newCtx("app", "r1"),
		proto.NextResultsReq{TID: "t", Since: 3, Epoch: testEpoch + 1}, rep))
	assert.True(t, rep.StaleEpoch)
	assert.EqualValues(t, testEpoch, rep.Epoch, "the caller is told which epoch is current")
	assert.Empty(t, rep.Results)

	// Zero asserts nothing, which is what a first read sends.
	rep = &proto.NextResultsRep{}
	assert.NoError(t, s.NextResults(newCtx("app", "r1"), proto.NextResultsReq{TID: "t"}, rep))
	assert.False(t, rep.StaleEpoch)
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
