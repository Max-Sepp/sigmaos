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
	for i := range scores {
		SleepBurn(iterDur)
		// Gradient 0: there is no useful "race a stalled trial" story for
		// hpsearch -- pruning the worst-ranked is the whole point, not
		// duplicating a stalled one.
		c.Score(scores[i], 0)
		db.DPrintf(db.HPSEARCH, "hp-trainer-vp config %d iter %d score %f", configId, i, scores[i])
	}

	c.Complete(Curve{ConfigId: configId, Seed: seed, Asymptote: asymptote, Scores: scores})
}
