package benchmarks_test

// The metrics-only ablation, on all three applications.
//
// The claim these exist to test is the introduction's central one: that cpu
// and memory metrics "do not encompass all the information a scaling decision
// could usefully use". Nothing in the evaluation was that policy. Comparing a
// classical application arm against a value-procs arm changes two things at
// once -- the signal the decision is made on, and the whole stack it is made
// in -- so a difference between them is attributable to either.
//
// Each test below runs one workload twice against the same scheduler, the same
// tree and the same admission machinery, changing only what value() is allowed
// to read (see valueprocs/policy/signal.go). Whatever separates the two arms is
// the information, because nothing else differs.
//
// The arms run sequentially in one realm rather than side by side, because
// there is one scheduler per realm by design. That makes each of these tests
// roughly twice the length of the single-arm test it is derived from.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"gonum.org/v1/gonum/mat"

	"sigmaos/apps/codedmatmul"
	"sigmaos/apps/hpsearch"
	"sigmaos/benchmarks"
	db "sigmaos/debug"
	"sigmaos/sigmaclnt"
	sp "sigmaos/sigmap"
	"sigmaos/test"
	"sigmaos/valueprocs/adapter"
	"sigmaos/valueprocs/clnt"
	"sigmaos/valueprocs/policy"
)

// withSignal runs body against a freshly started valuesched running under sig,
// and stops it afterwards.
//
// Starting and stopping around each arm is what keeps the comparison honest:
// the mode is fixed for the whole of a scheduler's life, so an arm that reused
// the previous arm's service would be measuring a scheduler that had already
// made decisions under the other signal.
func withSignal(sc *sigmaclnt.SigmaClnt, sig policy.Signal, body func(vpc *clnt.Clnt)) {
	vpjob := adapter.StartJobSignal(sc, 0, sig)
	defer vpjob.Stop()
	body(clnt.NewClnt(sc.FsLib))
}

// TestHPSearchSignalAblation asks what the reported scores buy a search.
//
// Under a metrics signal every trial is worth the same, so the ranking that
// decides which trials survive a squeeze falls through to age: the search
// keeps whatever started first. Under the value signal it keeps whatever is
// scoring best. Quality lost is the difference between those two, and it is
// the number the introduction's first claim predicts is nonzero.
func TestHPSearchSignalAblation(t *testing.T) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return
	}
	defer mrts.Shutdown()
	sc := mrts.GetRealm(REALM1).SigmaClnt

	cfg := hpsearch.DefaultConfig()
	if contentionEnabled() {
		cfg.Mem = HPSearchTrainerMem
	}

	ctn := startContention(t, sc)
	defer ctn.release()

	// One baseline, shared: both arms are measured against the same set of
	// curves, so the only thing that moves between them is the signal.
	baseCurves, err := runJob(sc, cfg)
	if !assert.Nil(t, err, "Error runJob (baseline): %v", err) {
		return
	}
	base := hpsearch.Analyze(baseCurves, cfg)

	run := func(sig policy.Signal) (*hpsearch.LivePruneResult, vpSummary, bool) {
		var (
			live  *hpsearch.LivePruneResult
			trace vpSummary
			ok    bool
		)
		withSignal(sc, sig, func(vpc *clnt.Clnt) {
			smp := startVPSampler(vpc)
			j, err := hpsearch.StartValueProcsJob(vpc, cfg)
			if !assert.Nil(t, err, "Error StartValueProcsJob (%v): %v", sig, err) {
				smp.summarize()
				return
			}
			outcomes, err := j.Wait()
			trace = smp.report("HPSearch signal=" + sig.String())
			if !assert.Nil(t, err, "Error Wait (%v): %v", sig, err) {
				return
			}
			live = hpsearch.AnalyzeLive(j.Curves(outcomes), cfg)
			db.DPrintf(db.ALWAYS, "HPSearch signal=%v: %d of %d trials finished", sig, hpsearch.NFinished(outcomes), cfg.NConfigs)
			ok = true
		})
		return live, trace, ok
	}

	value, valueTrace, ok := run(policy.SignalValue)
	if !ok {
		return
	}
	metrics, metricsTrace, ok := run(policy.SignalMetrics)
	if !ok {
		return
	}

	valueLost := base.BestQualityAll - value.BestQuality
	metricsLost := base.BestQualityAll - metrics.BestQuality
	db.DPrintf(db.ALWAYS, "HPSearch signal ablation: best-all %.3f; value best-kept %.3f (lost %.3f, %.2f core-s, width max=%d); metrics best-kept %.3f (lost %.3f, %.2f core-s, width max=%d)",
		base.BestQualityAll,
		value.BestQuality, valueLost, value.CoreSeconds, valueTrace.maxRunning,
		metrics.BestQuality, metricsLost, metrics.CoreSeconds, metricsTrace.maxRunning)
	db.DPrintf(db.ALWAYS, "HPSearch signal ablation: quality the reported scores bought = %.3f", metricsLost-valueLost)

	// Both arms have to resolve to a winner. Anything else is a broken arm
	// rather than a finding about signals.
	// At least one trial finished in each arm. Not "exactly one": a trial on
	// its last iteration can beat the stop that a completed sibling triggered,
	// and more than one finishing is a scheduling-latency observation rather
	// than a pruning failure.
	assert.True(t, value.NPruned < cfg.NConfigs, "value arm finished no trial")
	assert.True(t, metrics.NPruned < cfg.NConfigs, "metrics arm finished no trial")
}

// TestCodedMatMulSignalAblation asks what the reported scores buy a coded
// quorum.
//
// Both arms shed surplus once K workers finish, since that is the tree's
// structure rather than anything reported. What the signal should change is
// which surplus goes first when the cluster tightens: on value, the workers
// making least progress; on metrics, the ones that started most recently.
func TestCodedMatMulSignalAblation(t *testing.T) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return
	}
	defer mrts.Shutdown()
	sc := mrts.GetRealm(REALM1).SigmaClnt

	ctn := startContention(t, sc)
	defer ctn.release()

	cfg := codedmatmul.DefaultConfig()
	if contentionEnabled() {
		cfg.Mem = ContentionWorkerMem
	}
	r := cfg.M / cfg.K
	blocks, B := codedmatmul.GenBlocks(cfg.Seed, cfg.K, r, cfg.D, cfg.W)
	A := mat.NewDense(cfg.M, cfg.D, nil)
	for j, b := range blocks {
		A.Slice(j*r, (j+1)*r, 0, cfg.D).(*mat.Dense).Copy(b)
	}
	var want mat.Dense
	want.Mul(A, B)

	run := func(sig policy.Signal) (*codedmatmul.Result, vpSummary) {
		var (
			res   *codedmatmul.Result
			trace vpSummary
		)
		withSignal(sc, sig, func(vpc *clnt.Clnt) {
			smp := startVPSampler(vpc)
			res = runCodedMatMulValueProcsArm(t, vpc, cfg, &want, "signal="+sig.String())
			trace = smp.report("CodedMatMul signal=" + sig.String())
		})
		return res, trace
	}

	value, valueTrace := run(policy.SignalValue)
	metrics, metricsTrace := run(policy.SignalMetrics)
	if value == nil || metrics == nil {
		return
	}

	db.DPrintf(db.ALWAYS, "CodedMatMul signal ablation: N=%d K=%d; value makespan %v (width max=%d mean=%.2f); metrics makespan %v (width max=%d mean=%.2f)",
		cfg.N, cfg.K,
		value.Makespan, valueTrace.maxRunning, valueTrace.meanRunning,
		metrics.Makespan, metricsTrace.maxRunning, metricsTrace.meanRunning)

	// Both arms must still decode correctly and never exceed the tree's width
	// -- runCodedMatMulValueProcsArm checks the first, this checks the second.
	assert.True(t, valueTrace.maxRunning <= cfg.N && metricsTrace.maxRunning <= cfg.N,
		"An arm ran more workers than the tree has: value %d, metrics %d, N %d",
		valueTrace.maxRunning, metricsTrace.maxRunning, cfg.N)
}

// TestMRSignalAblation asks what the reported gradients buy straggler
// recovery, and it is the sharpest of the three.
//
// A metrics signal cannot tell a wedged attempt from a healthy one -- both
// hold exactly one slot -- so nothing it can see distinguishes the task that
// needs a backup from the nine that do not. The prediction is that the metrics
// arm degrades toward the no-recovery baseline while the value arm does not.
func TestMRSignalAblation(t *testing.T) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return
	}
	defer mrts.Shutdown()
	sc := mrts.GetRealm(REALM1).SigmaClnt

	run := func(sig policy.Signal) (dur time.Duration, mapStopped, reduceStopped int32) {
		withSignal(sc, sig, func(vpc *clnt.Clnt) {
			dur, mapStopped, reduceStopped = runMRValueProcsStragglerJob(mrts, vpc, StragglerSlowdownMs)
		})
		return
	}

	valueDur, valueMapStopped, valueReduceStopped := run(policy.SignalValue)
	metricsDur, metricsMapStopped, metricsReduceStopped := run(policy.SignalMetrics)

	rs := benchmarks.NewResults(1, benchmarks.E2E)
	rs.Append(valueDur, 1.0)
	printResultSummary(rs)

	db.DPrintf(db.ALWAYS, "MR signal ablation (task %d +%dms): value %v (map stops %d, reduce stops %d); metrics %v (map stops %d, reduce stops %d)",
		StragglerSlowTaskId, StragglerSlowdownMs,
		valueDur, valueMapStopped, valueReduceStopped,
		metricsDur, metricsMapStopped, metricsReduceStopped)
	db.DPrintf(db.ALWAYS, "MR signal ablation: time the reported gradients bought = %v -- compare against TestMRStragglerBaseline for the no-recovery figure",
		metricsDur-valueDur)
}
