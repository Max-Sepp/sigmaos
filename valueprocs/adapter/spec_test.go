package adapter

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"sigmaos/proc"
	"sigmaos/valueprocs"
	"sigmaos/valueprocs/policy"
	"sigmaos/valueprocs/proto"
)

func TestTemplateRejectsWhatCannotBeRerun(t *testing.T) {
	// A leaf may be stopped for capacity and re-run from scratch, so it must
	// be best-effort work that holds no reservation of its own.
	lc := proc.NewProc("sleeper", nil)
	lc.SetType(proc.T_LC)
	_, err := NewProcTemplate(lc)
	assert.Error(t, err)

	reserved := proc.NewProc("sleeper", nil)
	reserved.SetMcpu(1000)
	_, err = NewProcTemplate(reserved)
	assert.Error(t, err)

	_, err = NewProcTemplate(nil)
	assert.Error(t, err)

	ok, err := NewProcTemplate(proc.NewProc("sleeper", []string{"a"}))
	assert.NoError(t, err)
	assert.Equal(t, "sleeper", ok.Name())
}

func TestTemplateOutlivesTheProcItCaptured(t *testing.T) {
	p := proc.NewProc("sleeper", []string{"a"})
	p.AppendEnv("K", "v")
	tm, err := NewProcTemplate(p)
	assert.NoError(t, err)

	// The submitter still holds its proc and may do anything with it.
	p.AppendEnv("K", "changed")

	built := tm.Build(policy.RunRef{Tree: "t", Node: "r", Run: 0}, policy.Launch{})
	assert.Equal(t, "v", built.Env["K"])
}

func TestBuildCarriesResourcesFromTheTemplate(t *testing.T) {
	p := proc.NewProc("sleeper", nil)
	p.SetMem(256)
	tm, err := NewProcTemplate(p)
	assert.NoError(t, err)

	built := tm.Build(policy.RunRef{Tree: "t", Node: "r", Run: 3}, policy.Launch{})
	assert.EqualValues(t, 256, built.GetMem())
	assert.Equal(t, proc.T_BE, built.GetType())
	assert.EqualValues(t, 0, built.GetMcpu())
	assert.Equal(t, "3", built.Env[valueprocs.ENV_RUN])
}

func TestResultRehydrates(t *testing.T) {
	assert.Nil(t, result(nil))

	b := result(proc.NewStatusInfo(proc.StatusOK, "msg", map[string]any{"n": 1.0}))
	st := proc.NewStatusFromBytes(b)
	assert.True(t, st.IsStatusOK())
	assert.Equal(t, "msg", st.Msg())
	assert.Equal(t, map[string]any{"n": 1.0}, st.Data())
}

// leafSpec is a submittable leaf.
func leafSpec(label string) *proto.NodeSpec {
	return &proto.NodeSpec{
		Label:    label,
		LeafProc: proc.NewProc("sleeper", nil).GetProto(),
	}
}

func TestGroupFromProtoBuildsANestedTree(t *testing.T) {
	// The MapReduce shape: a quorum of everything, with one member the app
	// has declared may be raced.
	spec := &proto.NodeSpec{
		K: 3,
		Children: []*proto.NodeSpec{
			leafSpec("t1"),
			leafSpec("t2"),
			{K: 1, Label: "raced", Children: []*proto.NodeSpec{leafSpec("t3"), leafSpec("t3dup")}},
		},
	}

	g, err := GroupFromProto(spec)
	assert.NoError(t, err)

	// Submitting it is the real assertion: the scheduler accepts the shape
	// and derives a node per leaf, four of them under a root of three.
	s := policy.NewScheduler(policy.DefaultConfig(), nil, nil, nil)
	_, _, err = s.SubmitTree(time.Now(), policy.TreeSpec{ID: "t", Root: g})
	assert.NoError(t, err)

	v, ok := s.TreeView("t")
	assert.True(t, ok)
	assert.Len(t, v.Nodes, 6, "root, two leaves, the race node and its two leaves")

	leaves := 0
	for _, n := range v.Nodes {
		if n.IsLeaf {
			leaves++
		}
	}
	assert.Equal(t, 4, leaves)
}

func TestGroupFromProtoLabelsSurvive(t *testing.T) {
	// A label is how a client recognises a node in a report, since node
	// identity is derived from tree position rather than submitted.
	g, err := GroupFromProto(&proto.NodeSpec{
		K: 1, Label: "sweep", Children: []*proto.NodeSpec{leafSpec("trial-0")},
	})
	assert.NoError(t, err)

	s := policy.NewScheduler(policy.DefaultConfig(), nil, nil, nil)
	_, _, err = s.SubmitTree(time.Now(), policy.TreeSpec{ID: "t", Root: g})
	assert.NoError(t, err)

	v, _ := s.TreeView("t")
	assert.Equal(t, "sweep", v.Nodes[0].Label)
	assert.Equal(t, "trial-0", v.Nodes[1].Label)
}

func TestGroupFromProtoRejectsUnrunnableTrees(t *testing.T) {
	lc := proc.NewProc("sleeper", nil)
	lc.SetType(proc.T_LC)

	for _, tc := range []struct {
		name string
		spec *proto.NodeSpec
	}{
		{"nil node", nil},
		{"leaf with no proc", &proto.NodeSpec{}},
		{"both children and a proc", &proto.NodeSpec{
			K:        1,
			Children: []*proto.NodeSpec{leafSpec("a")},
			LeafProc: proc.NewProc("sleeper", nil).GetProto(),
		}},
		{"k below one", &proto.NodeSpec{K: 0, Children: []*proto.NodeSpec{leafSpec("a")}}},
		{"k above the children", &proto.NodeSpec{K: 2, Children: []*proto.NodeSpec{leafSpec("a")}}},
		{"a leaf that cannot be rerun", &proto.NodeSpec{K: 1, Children: []*proto.NodeSpec{
			{Label: "lc", LeafProc: lc.GetProto()},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Rejected here, at the caller, rather than halfway through a job.
			_, err := GroupFromProto(tc.spec)
			assert.Error(t, err)
		})
	}
}
