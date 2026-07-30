package adapter

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"sigmaos/valueprocs/policy"
)

func views(ids ...policy.TreeID) []policy.TreeView {
	out := make([]policy.TreeView, 0, len(ids))
	for _, id := range ids {
		out = append(out, policy.TreeView{ID: id})
	}
	return out
}

func TestFairShareDividesEvenly(t *testing.T) {
	got := FairShare{}.Arbitrate(views("a", "b", "c"), policy.Capacity{Slots: 9})
	assert.Equal(t, []policy.TreeShare{
		{Tree: "a", Slots: 3},
		{Tree: "b", Slots: 3},
		{Tree: "c", Slots: 3},
	}, got)
}

func TestFairShareGivesTheRemainderToTheOldest(t *testing.T) {
	// Trees arrive in submission order, so this breaks the tie in favour of
	// whatever has been waiting longest.
	got := FairShare{}.Arbitrate(views("a", "b", "c"), policy.Capacity{Slots: 11})
	assert.Equal(t, []policy.TreeShare{
		{Tree: "a", Slots: 4},
		{Tree: "b", Slots: 4},
		{Tree: "c", Slots: 3},
	}, got)
}

func TestFairShareNeverStarvesATreeToNothing(t *testing.T) {
	// Ten trees over four slots divides to zero, which would leave every one
	// of them below its own quorum and none of them able to finish.
	got := FairShare{}.Arbitrate(views("a", "b", "c", "d", "e", "f", "g", "h", "i", "j"),
		policy.Capacity{Slots: 4})
	assert.Len(t, got, 10)
	for _, sh := range got {
		assert.GreaterOrEqual(t, sh.Slots, 1, "%v", sh.Tree)
	}
}

func TestFairShareHonoursItsFloor(t *testing.T) {
	got := FairShare{Floor: 3}.Arbitrate(views("a", "b"), policy.Capacity{Slots: 2})
	assert.Equal(t, []policy.TreeShare{
		{Tree: "a", Slots: 3},
		{Tree: "b", Slots: 3},
	}, got)
}

func TestFairShareDeclinesToDivideTheUnknown(t *testing.T) {
	// No stated capacity is not the same as no capacity. Returning nothing
	// leaves every tree unlimited, which is what the scheduler assumes.
	assert.Nil(t, FairShare{}.Arbitrate(views("a"), policy.Capacity{Slots: 0}))
	assert.Nil(t, FairShare{}.Arbitrate(nil, policy.Capacity{Slots: 8}))
}

// The scheduler calls Arbitrate inline while reconciling, so it must not be
// the source of nondeterminism.
func TestFairShareIsDeterministic(t *testing.T) {
	v := views("a", "b", "c", "d")
	first := FairShare{}.Arbitrate(v, policy.Capacity{Slots: 7})
	for range 20 {
		assert.Equal(t, first, FairShare{}.Arbitrate(v, policy.Capacity{Slots: 7}))
	}
}
