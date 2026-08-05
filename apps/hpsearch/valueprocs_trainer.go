package hpsearch

import (
	db "sigmaos/debug"
	"sigmaos/valueprocs/vproc"
)

// RunValueProcsTrainer is the entry point for the hp-trainer-vp proc, the
// value-procs-scheduled counterpart of RunTrainer.
//
// Args are [configId, seed, maxIters, iterDurMs] -- the same as RunTrainer,
// minus the progressDir/margin RunPruningTrainer needs: there is no
// sibling-comparison protocol here, since the pruning decision moves
// entirely to the scheduler. The trainer only ever reports its own score;
// it never learns whether it was pruned.
func RunValueProcsTrainer(args []string) {
	if len(args) != 4 {
		db.DFatalf("RunValueProcsTrainer: wrong number of args %v", args)
	}
	configId, seed, maxIters, iterDur, err := parseTrainerArgs(args)
	if err != nil {
		db.DFatalf("RunValueProcsTrainer: %v", err)
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
		SleepBurn(iterDur)
		c.Score(scores[i], 0)
		db.DPrintf(db.HPSEARCH, "hp-trainer-vp config %d iter %d score %f", configId, i, scores[i])
	}

	c.Complete(Curve{ConfigId: configId, Seed: seed, Asymptote: asymptote, Scores: scores})
}
