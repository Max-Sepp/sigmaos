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
	"sigmaos/valueprocs/adapter"
	"sigmaos/valueprocs/clnt"
)

// runJob starts a baseline search and waits for it to finish.
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
	sc := mrts.GetRealm(REALM1).SigmaClnt

	db.DPrintf(db.ALWAYS, "TestHPSearchBaseline: cfg NConfigs=%d MaxIters=%d IterDur=%v (trainers reserve nothing)",
		cfg.NConfigs, cfg.MaxIters, cfg.IterDur)

	ctn := startContention(t, sc)
	defer ctn.release()

	// Run every config to completion (no pruning) and collect its curve.
	curves, err := runJob(sc, cfg)
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

	db.DPrintf(db.ALWAYS, "TestHPSearchLivePruning: cfg NConfigs=%d MaxIters=%d IterDur=%v Margin=%.3f (trainers reserve nothing)",
		cfg.NConfigs, cfg.MaxIters, cfg.IterDur, cfg.Margin)

	ctn := startContention(t, sc)
	defer ctn.release()

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

	// Per-config decision trace: each trainer reports its own Pruned/
	// PrunedAtIter (apps/hpsearch/trainer.go) directly in its exit status, so
	// this is the trainer's own decision, not a reconstruction.
	for _, c := range liveCurves {
		if c.Pruned {
			db.DPrintf(db.ALWAYS, "HPSearch live pruning: config %d pruned at iter %d/%d (score %.3f)",
				c.ConfigId, c.PrunedAtIter, cfg.MaxIters, c.Scores[len(c.Scores)-1])
		} else {
			db.DPrintf(db.ALWAYS, "HPSearch live pruning: config %d ran to completion (score %.3f)",
				c.ConfigId, c.Scores[len(c.Scores)-1])
		}
	}

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

// TestHPSearchValueProcs compares the baseline (no pruning) search against
// the value-procs-scheduled arm: a Select(1, NConfigs) tree whose scheduler
// prunes the worst-ranked configs as pressure rises, replacing pruner.go's
// peer-published-progress protocol entirely (see apps/hpsearch/valueprocs_job.go).
func TestHPSearchValueProcs(t *testing.T) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return
	}
	defer mrts.Shutdown()

	cfg := hpsearch.DefaultConfig()
	sc := mrts.GetRealm(REALM1).SigmaClnt

	vpjob := adapter.StartJob(sc, 0)
	defer vpjob.Stop()
	vpc := clnt.NewClnt(sc.FsLib)

	db.DPrintf(db.ALWAYS, "TestHPSearchValueProcs: cfg NConfigs=%d MaxIters=%d IterDur=%v (trainers reserve nothing)",
		cfg.NConfigs, cfg.MaxIters, cfg.IterDur)

	ctn := startContention(t, sc)
	defer ctn.release()

	// Baseline run: the "actual" cost of running everything to completion.
	baseCurves, err := runJob(sc, cfg)
	if !assert.Nil(t, err, "Error runJob (baseline): %v", err) {
		return
	}
	assert.Equal(t, cfg.NConfigs, len(baseCurves))
	base := hpsearch.Analyze(baseCurves, cfg)

	// Value-procs run: one winner reported at full fidelity, every other
	// config's curve approximately reconstructed from its last-known score
	// (see ValueProcsJob.Wait's doc comment for why an exact readback isn't
	// possible).
	// Sampled for the length of the run, because the tree dump below is taken
	// once everything has stopped and so cannot say how wide the search ever
	// got or against what pressure -- which is the quantity the pruning claim
	// is actually about.
	smp := startVPSamplerAt(vpc, ctn.startedAt)
	j, err := hpsearch.StartValueProcsJob(vpc, cfg)
	if !assert.Nil(t, err, "Error StartValueProcsJob: %v", err) {
		smp.summarize()
		return
	}
	outcomes, err := j.Wait()
	trace := smp.reportTrace("HPSearch value-procs")
	if !assert.Nil(t, err, "Error Wait: %v", err) {
		return
	}
	vpCurves := j.Curves(outcomes)
	assert.Equal(t, cfg.NConfigs, len(vpCurves))
	live := hpsearch.AnalyzeValueProcs(outcomes, vpCurves, cfg)
	best := hpsearch.Best(outcomes)
	if !assert.NotNil(t, best, "No trial finished") {
		return
	}
	db.DPrintf(db.ALWAYS, "HPSearch value-procs selection: %d of %d trials finished, application picked config %d",
		hpsearch.NFinished(outcomes), cfg.NConfigs, best.ConfigId)

	db.DPrintf(db.ALWAYS, "HPSearch value-procs: actual %.2f core-s, value-procs %.2f core-s (saved %.1f%%), %d/%d configs pruned",
		base.ActualCoreSeconds, live.CoreSeconds, (base.ActualCoreSeconds-live.CoreSeconds)/base.ActualCoreSeconds*100,
		live.NPruned, cfg.NConfigs)
	db.DPrintf(db.ALWAYS, "HPSearch value-procs quality: best-all %.3f, best-kept %.3f, quality lost %.3f, width max=%d mean=%.2f of %d slots, pressure max=%.3f mean=%.3f",
		base.BestQualityAll, live.BestQuality, base.BestQualityAll-live.BestQuality,
		trace.maxRunning, trace.meanRunning, trace.slots, trace.maxPressure, trace.meanPressure)

	if st, err := j.Status(); err == nil {
		db.DPrintf(db.ALWAYS, "HPSearch value-procs final tree: %s", dumpTreeStatus(st))
	} else {
		db.DPrintf(db.ALWAYS, "TestHPSearchValueProcs: Status err %v", err)
	}

	// Every trial that did not finish was pruned, and at least one finished.
	//
	// Not "exactly one finished": a Select(1, N) stops the others once one
	// completes, but a trial already on its last iteration can finish inside
	// the window before that stop lands. Asserting a single winner made the
	// test a check on scheduling latency rather than on pruning.
	assert.Equal(t, cfg.NConfigs-hpsearch.NFinished(outcomes), live.NPruned,
		"Every trial that did not finish should be counted pruned")
	assert.True(t, hpsearch.NFinished(outcomes) >= 1, "Expected at least one trial to finish")
	assert.True(t, live.CoreSeconds <= base.ActualCoreSeconds, "Value-procs run used more compute than the baseline")
}
