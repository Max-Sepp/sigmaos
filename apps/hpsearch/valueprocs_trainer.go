package hpsearch

import (
	"strconv"

	db "sigmaos/debug"
	"sigmaos/util/burn"
	"sigmaos/valueprocs/vproc"
)

// RunValueProcsTrainer is the entry point for the hp-trainer-vp proc, the
// value-procs-scheduled counterpart of RunTrainer.
//
// Args are [configId, seed, maxIters, iterDurMs, scoreScale, silent] -- the
// first four the same as RunTrainer, minus the progressDir/margin
// RunPruningTrainer needs: there is no sibling-comparison protocol here,
// since the pruning decision moves entirely to the scheduler. The trainer
// only ever reports its own score; it never learns whether it was pruned.
//
// The last two are negative controls (see Config.InflateConfig and
// Config.SilentConfig). scoreScale multiplies what is reported; silent
// withholds reports until the run completes. Neither changes the Curve
// returned at the end, so a test can watch the scheduler be misled while
// still measuring quality against the truth.
func RunValueProcsTrainer(args []string) {
	if len(args) != 6 {
		db.DFatalf("RunValueProcsTrainer: wrong number of args %v", args)
	}
	configId, seed, maxIters, iterDur, err := parseTrainerArgs(args)
	if err != nil {
		db.DFatalf("RunValueProcsTrainer: %v", err)
	}
	scale, err := strconv.ParseFloat(args[4], 64)
	if err != nil {
		db.DFatalf("RunValueProcsTrainer: scoreScale %v not a float: %v", args[4], err)
	}
	silent, err := strconv.ParseBool(args[5])
	if err != nil {
		db.DFatalf("RunValueProcsTrainer: silent %v not a bool: %v", args[5], err)
	}
	db.DPrintf(db.HPSEARCH, "hp-trainer-vp start config %d seed %d maxIters %d iterDur %v args %v", configId, seed, maxIters, iterDur, args)

	c, err := vproc.Start()
	if err != nil {
		db.DFatalf("RunValueProcsTrainer: vproc.Start: %v", err)
	}

	// Generate the full curve upfront, exactly as RunTrainer does.
	asymptote, scores := syntheticCurve(seed, maxIters)

	// Gradient 0, and it is a claim rather than an omission.
	//
	// The scheduler reads a gradient as the slope of the curve it selects on,
	// and extrapolates along it: score + gradient*w. What this tree selects on
	// is which config is best, so the curve that matters is a trial's eventual
	// quality -- and a trainer has no honest slope for that. Its score is a
	// running lower bound on where it will end up rather than an estimate of
	// it, and the two have entirely different derivatives.
	//
	// Reporting d(score)/dt in its place is actively harmful, and measurably
	// so. A learning curve is concave, so its slope is steepest at the start,
	// which is precisely the window in which all but one of these configs are
	// pruned. Extrapolating linearly from a tangent taken there ranks configs
	// by how fast they climb rather than by where they finish, which keeps a
	// config that plateaus early over one that is still climbing towards a
	// better result.
	//
	// Reporting 0 leaves the scheduler ranking on realized quality, which is
	// what a search wants. Note that this is a statement about what a trainer
	// can honestly claim, not about racing: any claim at all about the slope of
	// a concave curve would make the ranking follow something other than
	// quality.
	for i := range scores {
		burn.For(iterDur)
		// A silent trainer runs the same work and reports none of it, which is
		// what an application that only knows its answer at the end looks like
		// to the scheduler. It is scored once, on completion, below.
		if !silent {
			c.Score(scores[i]*scale, 0)
		}
		db.DPrintf(db.HPSEARCH, "hp-trainer-vp config %d iter %d score %f reported %v", configId, i, scores[i], !silent)
	}

	// Whatever it withheld or inflated on the way, the curve handed back is
	// the true one: the negative controls corrupt the scheduler's view of this
	// trial, never the record a benchmark measures quality against.
	c.Complete(Curve{ConfigId: configId, Seed: seed, Asymptote: asymptote, Scores: scores})
}
