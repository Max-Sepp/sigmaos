package hpsearch

// Pure analysis of the learning curves produced by hyperparameter search jobs.

import (
	"math"
	"time"

	db "sigmaos/debug"
)

// TargetFrac is the time-to-target-accuracy threshold, as a fraction of the
// best score any config reaches.
const TargetFrac = 0.9

// Result summarizes a baseline hyperparameter search (no pruning): what the
// run actually cost and the quality it reached, so a live-pruning run can be
// set against it.
type Result struct {
	ActualCoreSeconds float64
	// Best score with every config run to completion.
	BestQualityAll float64
	// When the search first reaches TargetFrac*BestQualityAll.
	ItersToTarget   int
	SecsToTarget    float64
	PerConfigCurves []*Curve
}

// LiveResult summarizes a live-pruning run (StartPruningJob): the numbers the
// causal policy actually achieved, to be set against a Result computed from a
// baseline run of the same config.
type LiveResult struct {
	CoreSeconds float64
	NPruned     int
	BestQuality float64
}

// BestScore returns the highest score in s[:n], or -Inf for n<=0.
func BestScore(s []float64, n int) float64 {
	if n > len(s) {
		n = len(s)
	}
	best := math.Inf(-1)
	for i := 0; i < n; i++ {
		if s[i] > best {
			best = s[i]
		}
	}
	return best
}

// BestQuality returns the highest score reached by any of the curves.
func BestQuality(curves []*Curve) float64 {
	best := math.Inf(-1)
	for _, c := range curves {
		if q := BestScore(c.Scores, len(c.Scores)); q > best {
			best = q
		}
	}
	return best
}

// CoreSeconds returns the total compute the curves represent: one core for
// iterDur per iteration actually run. A pruned curve has a truncated
// Scores, so it contributes only the iterations it got through.
func CoreSeconds(curves []*Curve, iterDur time.Duration) float64 {
	total := 0.0
	for _, c := range curves {
		total += float64(len(c.Scores)) * iterDur.Seconds()
	}
	return total
}

// NumPruned counts how many configs the live pruning policy cut short.
func NumPruned(curves []*Curve) int {
	n := 0
	for _, c := range curves {
		if c.Pruned {
			n++
		}
	}
	return n
}

// FirstIterToTarget returns the earliest iteration at which any curve reaches
// target, and whether any did. A pruned curve has a truncated Scores, so it
// can't reach target past its prune point.
func FirstIterToTarget(curves []*Curve, target float64) (int, bool) {
	best := -1
	for _, c := range curves {
		for i, s := range c.Scores {
			if s >= target {
				if best < 0 || i < best {
					best = i
				}
				break
			}
		}
	}
	return best, best >= 0
}

// Analyze computes the baseline summary for a completed baseline job (no
// pruning): what the run actually cost and the quality it reached.
func Analyze(curves []*Curve, cfg *Config) *Result {
	iterSec := cfg.IterDur.Seconds()
	for _, c := range curves {
		db.DPrintf(db.HPSEARCH, "hpsearch config %d asymptote %f iters %d", c.ConfigId, c.Asymptote, len(c.Scores))
	}
	actual := CoreSeconds(curves, cfg.IterDur)
	bestAll := BestQuality(curves)
	itersToTarget, _ := FirstIterToTarget(curves, TargetFrac*bestAll)
	return &Result{
		ActualCoreSeconds: actual,
		BestQualityAll:    bestAll,
		ItersToTarget:     itersToTarget,
		SecsToTarget:      float64(itersToTarget) * iterSec,
		PerConfigCurves:   curves,
	}
}

// AnalyzeLive tallies what a live-pruning run actually spent and how many
// of its configs got pruned.
func AnalyzeLive(curves []*Curve, cfg *Config) *LiveResult {
	for _, c := range curves {
		db.DPrintf(db.HPSEARCH, "hpsearch-live config %d asymptote %f pruned %v at %d/%d",
			c.ConfigId, c.Asymptote, c.Pruned, c.PrunedAtIter, cfg.MaxIters)
	}
	return &LiveResult{
		CoreSeconds: CoreSeconds(curves, cfg.IterDur),
		NPruned:     NumPruned(curves),
		BestQuality: BestQuality(curves),
	}
}
