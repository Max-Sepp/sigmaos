package clnt

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"sigmaos/proc"
	"sigmaos/valueprocs/adapter"
	"sigmaos/valueprocs/policy"
)

func leaf(program string) *WorkNode { return Leaf(proc.NewProc(program, nil)) }

func TestSelectRejectsWhatCannotBeScheduled(t *testing.T) {
	_, err := Select(1)
	assert.Error(t, err, "no children")

	_, err = Select(0, leaf("a"))
	assert.Error(t, err, "k below one")

	_, err = Select(2, leaf("a"))
	assert.Error(t, err, "k above the children")

	_, err = Select(1, leaf("a"), nil)
	assert.Error(t, err, "a nil child")

	_, err = Select(1, leaf("a"))
	assert.NoError(t, err)
}

// TestTheTreeAnApplicationBuildsIsTheTreeTheSchedulerRuns closes the loop the
// wire format sits in the middle of. An application describes work here; the
// scheduler has to end up with exactly that shape, or the two halves of this
// layer disagree about what was asked for.
func TestTheTreeAnApplicationBuildsIsTheTreeTheSchedulerRuns(t *testing.T) {
	raced, err := Select(1, leaf("t3"), leaf("t3dup"))
	assert.NoError(t, err)
	root, err := Select(3, leaf("t1"), leaf("t2"), raced.WithLabel("raced"))
	assert.NoError(t, err)

	g, err := adapter.GroupFromProto(root.spec)
	assert.NoError(t, err)

	s := policy.NewScheduler(policy.DefaultConfig(), nil, nil, nil)
	_, _, err = s.SubmitTree(t0(), policy.TreeSpec{ID: "t", Root: g})
	assert.NoError(t, err)

	v, ok := s.TreeView("t")
	assert.True(t, ok)
	assert.Len(t, v.Nodes, 6, "root, two leaves, the race node, and its two leaves")
	assert.Equal(t, 3, v.Nodes[0].K)

	leaves := 0
	for _, n := range v.Nodes {
		if n.IsLeaf {
			leaves++
		}
	}
	assert.Equal(t, 4, leaves)

	// The label survives the round trip, which is the only way a report about
	// "r.2" becomes a report about something an application named.
	var labelled int
	for _, n := range v.Nodes {
		if n.Label == "raced" {
			labelled++
			assert.False(t, n.IsLeaf)
			assert.Equal(t, 1, n.K)
		}
	}
	assert.Equal(t, 1, labelled)
}

func TestLeafCarriesItsProc(t *testing.T) {
	p := proc.NewProc("sleeper", []string{"1s"})
	n := Leaf(p).WithLabel("trial-7")

	assert.Equal(t, "trial-7", n.spec.Label)
	assert.Empty(t, n.spec.Children)
	assert.Equal(t, p.GetProto(), n.spec.LeafProc)

	// And the adapter accepts it, which is where idempotence is checked.
	_, err := adapter.GroupFromProto(n.spec)
	assert.NoError(t, err)
}

func TestALeafThatCannotBeRerunIsRejected(t *testing.T) {
	// The scheduler may stop a leaf for capacity and run it again, so a leaf
	// holding a reservation it never told anyone about cannot be scheduled.
	p := proc.NewProc("sleeper", nil)
	p.SetMcpu(1000)
	_, err := adapter.GroupFromProto(Leaf(p).spec)
	assert.Error(t, err)
}

func TestCursorIsAPairNotANumber(t *testing.T) {
	// The zero value asks from the beginning and asserts nothing about which
	// incarnation it came from, which is what a first read sends.
	var cur Cursor
	assert.Zero(t, cur.Epoch)
	assert.Zero(t, cur.Since)
}

func t0() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
