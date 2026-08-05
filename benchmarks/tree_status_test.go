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
func dumpTreeStatus(st *proto.TreeStatusRep) string {
	s := fmt.Sprintf("tree %s state=%s cancelled=%v nRunning=%d nCharged=%d",
		st.TID, st.State, st.Cancelled, st.NRunning, st.NCharged)
	for _, n := range st.Nodes {
		if !n.IsLeaf {
			continue
		}
		s += fmt.Sprintf("\n  leaf %s label=%s state=%s runState=%s run=%d attempts=%d stops=%d hasScore=%v score=%.3f scoreStale=%v pid=%s",
			n.NodeID, n.Label, n.State, n.RunState, n.Run, n.Attempts, n.Stops, n.HasScore, n.Score, n.ScoreStale, n.PID)
	}
	return s
}
