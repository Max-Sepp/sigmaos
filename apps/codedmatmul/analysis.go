package codedmatmul

// Pure analysis of the per-worker results a coded-matmul Job.Wait collects.

import (
	"slices"
	"time"
)

type QuorumStats struct {
	Makespan time.Duration // Coordinator observed time to reach the K-quorum
	NEvicted int           // Number of surplus workers evicted at the K-quorum

	// QuorumIdx is which workers finished first, in index order -- the K whose
	// completion ended the wait.
	//
	// Distinct from which blocks Decode reads: Decode takes the K
	// lowest-numbered blocks available once everything has settled, so on a
	// run where the surplus is left alive every worker eventually finishes and
	// the decode set is always 0..K-1. Only this set says whom the job did not
	// have to wait for.
	QuorumIdx []int
}

// Sample is one worker's reported result plus whether it was among the K
// results Decode actually used to reconstruct C.
type Sample struct {
	Idx  int
	WR   *WorkerResult
	Used bool
}

// Result summarizes what a coded-matmul run actually cost.
type Result struct {
	Makespan          time.Duration
	TotalCoreSeconds  float64
	WastedCoreSeconds float64 // core-seconds spent by workers not used in the decode
	NEvicted          int

	// QuorumIdx is which workers the job did not have to wait past. It is the
	// direct evidence of what coding buys: with N > K the quorum can form
	// without whichever workers are slow, and this says whether it did.
	// Unlike the timings it does not depend on how loaded the host was.
	QuorumIdx []int
}

// InQuorum reports whether worker idx was among those whose completion ended
// the wait.
func (r *Result) InQuorum(idx int) bool { return slices.Contains(r.QuorumIdx, idx) }

// Analyze computes core-seconds spent (total and wasted-on-surplus) from a
// job's per-worker samples and the QuorumStats Wait observed.
//
// A running worker is charged one core. It used to be charged the Mcpu it
// declared, but no arm declares one any more (see Config), and charging what
// was declared would have scored every arm at zero -- while what a worker
// burns is a busy core either way, which is the quantity the comparison
// between arms is about.
func Analyze(samples []*Sample, stats QuorumStats) *Result {
	total, wasted := 0.0, 0.0
	for _, s := range samples {
		if s.WR == nil {
			continue
		}
		cs := s.WR.Elapsed.Seconds()
		total += cs
		if !s.Used {
			wasted += cs
		}
	}
	quorum := slices.Clone(stats.QuorumIdx)
	slices.Sort(quorum)
	return &Result{
		Makespan:          stats.Makespan,
		TotalCoreSeconds:  total,
		WastedCoreSeconds: wasted,
		NEvicted:          stats.NEvicted,
		QuorumIdx:         quorum,
	}
}
