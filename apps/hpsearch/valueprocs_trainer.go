package hpsearch

import (
	"strconv"

	db "sigmaos/debug"
	"sigmaos/proc"
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

	// Graceful, so that a pruned trial can say how far it got. That number is
	// the only account of what the trial cost -- see TrialProgress -- and a
	// proc that dies on the eviction takes it with it.
	c, err := vproc.Start(vproc.WithGracefulEvict())
	if err != nil {
		db.DFatalf("RunValueProcsTrainer: vproc.Start: %v", err)
	}
	prior := priorIters(c.ResumeToken())

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
		// Before the burn rather than after, so that i is the count of
		// iterations that finished rather than one that includes the one this
		// attempt was interrupted partway through.
		if c.Cancelled() {
			db.DPrintf(db.HPSEARCH, "hp-trainer-vp config %d stopped at iter %d of %d (%d prior)", configId, i, len(scores), prior)
			c.StoppedWith(TrialProgress{ConfigId: configId, Iters: prior + i})
			return
		}
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

// priorIters is what earlier stopped attempts at this config already spent.
//
// A token that will not decode is reported and treated as nothing, because
// the alternative is to fail a trial that is running perfectly well over an
// accounting field. It undercounts that config's cost, which the log names.
func priorIters(tok []byte) int {
	if len(tok) == 0 {
		return 0
	}
	st, err := proc.StatusFromBytes(tok)
	if err != nil || st == nil {
		db.DPrintf(db.HPSEARCH, "hp-trainer-vp: resume token of %d bytes did not unmarshal: %v", len(tok), err)
		return 0
	}
	p, err := NewTrialProgress(st.Data())
	if err != nil {
		db.DPrintf(db.HPSEARCH, "hp-trainer-vp: resume token %v: %v", st, err)
		return 0
	}
	return p.Iters
}
