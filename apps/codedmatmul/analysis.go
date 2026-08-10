package codedmatmul

// Pure analysis of the per-worker results a coded-matmul Job.Wait collects.

import (
	"time"
)

type QuorumStats struct {
	Makespan time.Duration // Coordinator observed time to reach the K-quorum
	NEvicted int           // Number of surplus workers evicted at the K-quorum
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
}

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
	return &Result{
		Makespan:          stats.Makespan,
		TotalCoreSeconds:  total,
		WastedCoreSeconds: wasted,
		NEvicted:          stats.NEvicted,
	}
}
