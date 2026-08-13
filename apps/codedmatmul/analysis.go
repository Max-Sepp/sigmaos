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
	Makespan time.Duration

	// Slabs is what the run computed, and is the figure to compare arms on.
	//
	// A slab is one TiledMultiply tile: r by D/T of A against D/T by W of B,
	// the same size for every worker of every arm in a run, since all four are
	// handed the same M, D, W and T. So a slab count is an exact measure of
	// work with no rate to estimate and nothing to calibrate, and two arms that
	// computed the same thing report the same number however they were
	// scheduled.
	TotalSlabs  int
	WastedSlabs int // slabs computed by workers not used in the decode

	// ResidencySeconds is how long workers were alive, summed. It is not work.
	//
	// A worker sharing a core with two others is resident three times as long
	// as it computes, so this rises with how wide an arm runs -- which is the
	// property under test, making it exactly the wrong thing to charge an arm
	// for. Nine workers on four cores read about 40% more per unit of work than
	// six do. Kept because what a worker occupied is a real quantity and the
	// makespan does not capture it, but the comparison belongs on Slabs.
	TotalResidencySeconds  float64
	WastedResidencySeconds float64

	NEvicted int

	// QuorumIdx is which workers the job did not have to wait past. It is the
	// direct evidence of what coding buys: with N > K the quorum can form
	// without whichever workers are slow, and this says whether it did.
	// Unlike the timings it does not depend on how loaded the host was.
	QuorumIdx []int
}

// InQuorum reports whether worker idx was among those whose completion ended
// the wait.
func (r *Result) InQuorum(idx int) bool { return slices.Contains(r.QuorumIdx, idx) }

// Analyze computes what a job cost, in work and in residency, from its
// per-worker samples and the QuorumStats Wait observed.
//
// Both are reported because they are different quantities and only one of them
// is a cost the arms can be ranked on. Slabs is what was computed. Residency is
// how long workers were alive, which on an oversubscribed host is the same work
// stretched by however many of them were sharing a core -- so an arm that runs
// N workers where another runs K is charged for the width rather than for the
// work, and the width is what the comparison is supposed to be measuring.
func Analyze(samples []*Sample, stats QuorumStats) *Result {
	var (
		slabs, wastedSlabs int
		resident, wastedResident float64
	)
	for _, s := range samples {
		if s.WR == nil {
			continue
		}
		slabs += s.WR.SlabsDone
		resident += s.WR.Elapsed.Seconds()
		if !s.Used {
			wastedSlabs += s.WR.SlabsDone
			wastedResident += s.WR.Elapsed.Seconds()
		}
	}
	quorum := slices.Clone(stats.QuorumIdx)
	slices.Sort(quorum)
	return &Result{
		Makespan:               stats.Makespan,
		TotalSlabs:             slabs,
		WastedSlabs:            wastedSlabs,
		TotalResidencySeconds:  resident,
		WastedResidencySeconds: wastedResident,
		NEvicted:               stats.NEvicted,
		QuorumIdx:              quorum,
	}
}
