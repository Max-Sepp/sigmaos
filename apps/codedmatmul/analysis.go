package codedmatmul

// Pure analysis of the per-worker results a coded-matmul Job.Wait collects.

import (
	"time"

	"sigmaos/proc"
)

// RunStats is the coordinator-observed timing/reap info from one Job.Wait
// call: when the K-quorum was reached, and how many surplus workers were
// evicted at that point (0 unless Wait was called with cancelSurplus).
type RunStats struct {
	Makespan time.Duration
	NEvicted int
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
// job's per-worker samples, given the Mcpu each worker was given and the
// RunStats Wait observed.
func Analyze(samples []*Sample, mcpu proc.Tmcpu, stats RunStats) *Result {
	coreFrac := float64(mcpu) / 1000.0
	total, wasted := 0.0, 0.0
	for _, s := range samples {
		if s.WR == nil {
			continue
		}
		cs := s.WR.Elapsed.Seconds() * coreFrac
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
