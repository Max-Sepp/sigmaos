package benchmarks_test

// Shared value-procs tree introspection, so every value-procs benchmark arm
// (CodedMatMul, HPSearch, MR) can log the same per-leaf decision detail
// instead of only the aggregate counts (NAttemptsStopped, sumLeafStops, etc)
// each app already reports.

import (
	"fmt"

	"sigmaos/valueprocs/proto"
)

// dumpTreeStatus formats one line per node of a tree, with enough of
// NodeStatus to tell a genuinely wedged leaf (charged, no score movement, no
// PID any more) apart from one that is merely slow, and to retrace which
// leaves the scheduler stopped/re-ran and how many times.
//
// Interior nodes are printed too, for one field: target. A leaf's target means
// nothing, while how far past its quorum a Select reached is what decides
// whether a duplicate runs at all -- so a dump of leaves alone cannot say why
// one did not run. The alternative is inferring it from which leaves happen to
// appear with runState=running across successive dumps, which answers the
// question only indirectly.
//
// Gradient is printed beside score for the same reason. The two are one
// report: a point and the slope of the value curve there, which together are
// what the scheduler extrapolates along. A leaf sitting at score 0.000 tells
// you nothing on its own about whether it is expected to move.
func dumpTreeStatus(st *proto.TreeStatusRep) string {
	s := fmt.Sprintf("tree %s state=%s cancelled=%v nRunning=%d nCharged=%d",
		st.TID, st.State, st.Cancelled, st.NRunning, st.NCharged)
	for _, n := range st.Nodes {
		if !n.IsLeaf {
			s += fmt.Sprintf("\n  node %s label=%s state=%s k=%d nChildren=%d target=%d nRunning=%d nCharged=%d",
				n.NodeID, n.Label, n.State, n.K, n.NChildren, n.Target, n.NRunning, n.NCharged)
			continue
		}
		s += fmt.Sprintf("\n  leaf %s label=%s state=%s runState=%s run=%d attempts=%d stops=%d hasScore=%v score=%.3f gradient=%.3f scoreStale=%v pid=%s",
			n.NodeID, n.Label, n.State, n.RunState, n.Run, n.Attempts, n.Stops, n.HasScore, n.Score, n.Gradient, n.ScoreStale, n.PID)
	}
	return s
}
