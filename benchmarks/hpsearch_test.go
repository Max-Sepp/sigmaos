package benchmarks_test

// The two hpsearch benchmarks. Everything these tests do beyond booting a
// realm and asserting lives in apps/hpsearch: starting and reaping a job
// (StartNoPruneJob, StartPruningJob, HPSearchJob.Wait) and analyzing the
// curves it produces (Analyze, AnalyzeLive).

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"sigmaos/apps/hpsearch"
	db "sigmaos/debug"
	"sigmaos/sigmaclnt"
	sp "sigmaos/sigmap"
	"sigmaos/test"
)

// runJob starts a baseline search and waits for it to finish. Neither
// benchmark here adds contention while the search runs, so they use this
// rather than driving StartNoPruneJob/HPSearchJob.Wait themselves.
func runJob(sc *sigmaclnt.SigmaClnt, cfg *hpsearch.Config) ([]*hpsearch.Curve, error) {
	j, err := hpsearch.StartNoPruneJob(sc, cfg)
	if err != nil {
		return nil, err
	}
	return j.Wait()
}

// runPruningJob starts a live-pruning search and waits for it to finish,
// exactly like runJob but with the hp-trainer-pruned variant.
func runPruningJob(sc *sigmaclnt.SigmaClnt, cfg *hpsearch.Config) ([]*hpsearch.Curve, error) {
	j, err := hpsearch.StartPruningJob(sc, cfg)
	if err != nil {
		return nil, err
	}
	return j.Wait()
}

func TestHPSearchBaseline(t *testing.T) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return
	}
	defer mrts.Shutdown()

	cfg := hpsearch.DefaultConfig()

	// Run every config to completion (no pruning) and collect its curve.
	curves, err := runJob(mrts.GetRealm(REALM1).SigmaClnt, cfg)
	if !assert.Nil(t, err, "Error runJob: %v", err) {
		return
	}
	assert.Equal(t, cfg.NConfigs, len(curves))

	// Summarize what the baseline run cost and the quality it reached.
	res := hpsearch.Analyze(curves, cfg)

	curvesPath := "/tmp/" + t.Name() + "-curves.csv"
	if err := hpsearch.DumpCurvesCSV(curves, curvesPath); err != nil {
		db.DPrintf(db.ALWAYS, "HPSearch baseline: DumpCurvesCSV err %v", err)
	} else {
		db.DPrintf(db.ALWAYS, "HPSearch baseline: curves dumped to %v", curvesPath)
	}

	db.DPrintf(db.ALWAYS, "HPSearch baseline: actual %.2f core-s", res.ActualCoreSeconds)
	db.DPrintf(db.ALWAYS, "HPSearch baseline quality: best-all %.3f; time-to-target (%.0f%% of best) %d iters / %.2f core-s",
		res.BestQualityAll, hpsearch.TargetFrac*100, res.ItersToTarget, res.SecsToTarget)

	assert.True(t, res.ActualCoreSeconds > 0, "Expected the baseline run to use some compute, used %v", res.ActualCoreSeconds)
}

func TestHPSearchLivePruning(t *testing.T) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return
	}
	defer mrts.Shutdown()

	cfg := hpsearch.DefaultConfig()
	sc := mrts.GetRealm(REALM1).SigmaClnt

	// Baseline run: the "actual" cost of running everything to completion,
	// and the full curves the live run is set against.
	baseCurves, err := runJob(sc, cfg)
	if !assert.Nil(t, err, "Error runJob (baseline): %v", err) {
		return
	}
	assert.Equal(t, cfg.NConfigs, len(baseCurves))
	base := hpsearch.Analyze(baseCurves, cfg)

	// Live-pruning run: same config/seeds, but trainers prune themselves
	// online against each other's published progress.
	liveCurves, err := runPruningJob(sc, cfg)
	if !assert.Nil(t, err, "Error runPruningJob (live): %v", err) {
		return
	}
	assert.Equal(t, cfg.NConfigs, len(liveCurves))
	live := hpsearch.AnalyzeLive(liveCurves, cfg)

	basePath := "/tmp/" + t.Name() + "-baseline-curves.csv"
	if err := hpsearch.DumpCurvesCSV(baseCurves, basePath); err != nil {
		db.DPrintf(db.ALWAYS, "HPSearch live pruning: DumpCurvesCSV (baseline) err %v", err)
	} else {
		db.DPrintf(db.ALWAYS, "HPSearch live pruning: baseline curves dumped to %v", basePath)
	}
	livePath := "/tmp/" + t.Name() + "-live-curves.csv"
	if err := hpsearch.DumpCurvesCSV(liveCurves, livePath); err != nil {
		db.DPrintf(db.ALWAYS, "HPSearch live pruning: DumpCurvesCSV (live) err %v", err)
	} else {
		db.DPrintf(db.ALWAYS, "HPSearch live pruning: live curves dumped to %v", livePath)
	}

	// Both runs use the same absolute target so their times are comparable.
	target := hpsearch.TargetFrac * base.BestQualityAll
	baseIters, _ := hpsearch.FirstIterToTarget(baseCurves, target)
	liveIters, liveHit := hpsearch.FirstIterToTarget(liveCurves, target)

	db.DPrintf(db.ALWAYS, "HPSearch live pruning: actual %.2f core-s, live-pruned %.2f core-s (saved %.1f%%), %d/%d configs pruned",
		base.ActualCoreSeconds, live.CoreSeconds, (base.ActualCoreSeconds-live.CoreSeconds)/base.ActualCoreSeconds*100,
		live.NPruned, cfg.NConfigs)
	db.DPrintf(db.ALWAYS, "HPSearch live pruning quality: best-all %.3f, best-live-kept %.3f, quality lost %.3f; time-to-target (%.0f%% of best) baseline %d iters vs live %d iters (live reached target: %v)",
		base.BestQualityAll, live.BestQuality, base.BestQualityAll-live.BestQuality, hpsearch.TargetFrac*100, baseIters, liveIters, liveHit)

	// A sanity check only: every pruned curve is a strict prefix of the full
	// one, so live.CoreSeconds <= base.ActualCoreSeconds holds by construction.
	assert.True(t, live.CoreSeconds <= base.ActualCoreSeconds, "Live-pruned run used more compute than the baseline")
	assert.True(t, live.NPruned > 0, "Expected the live policy to prune at least one config")
}
