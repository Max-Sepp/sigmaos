package benchmarks_test

// The coded-matmul benchmark's three arms. Everything beyond booting a realm
// and asserting lives in apps/codedmatmul: starting and reaping a run
// (StartJob, Job.Wait) and analyzing what it cost (Analyze). The reference C
// used for the correctness check is computed once here, directly, via a single
// gonum Mul over the same A/B apps/codedmatmul.GenBlocks reconstructs
// deterministically from the shared seed.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"gonum.org/v1/gonum/mat"

	"sigmaos/apps/codedmatmul"
	db "sigmaos/debug"
	"sigmaos/sigmaclnt"
	sp "sigmaos/sigmap"
	"sigmaos/test"
	"sigmaos/valueprocs/adapter"
	"sigmaos/valueprocs/clnt"
)

// runCodedMatMulArm starts one coded-matmul run, waits for it, and checks the
// decoded result against the reference C. Returns nil (after recording a test
// failure) if anything along the way didn't hold.
func runCodedMatMulArm(t *testing.T, sc *sigmaclnt.SigmaClnt, cfg *codedmatmul.Config, cancelSurplus bool, want *mat.Dense, name string) *codedmatmul.Result {
	j, err := codedmatmul.StartJob(sc, cfg)
	if !assert.Nil(t, err, "%s: StartJob err %v", name, err) {
		return nil
	}
	C, samples, stats, err := j.Wait(cancelSurplus)
	if !assert.Nil(t, err, "%s: Wait err %v", name, err) {
		return nil
	}
	if !assert.True(t, mat.EqualApprox(C, want, 1e-6), "%s: decoded C does not match the reference", name) {
		return nil
	}
	res := codedmatmul.Analyze(samples, cfg.Mcpu, stats)
	db.DPrintf(db.ALWAYS, "CodedMatMul %s: makespan %v, total %.2f core-s, wasted %.2f core-s, %d evicted",
		name, res.Makespan, res.TotalCoreSeconds, res.WastedCoreSeconds, res.NEvicted)
	return res
}

// runCodedMatMulValueProcsArm is runCodedMatMulArm's counterpart for the
// value-procs-scheduled arm: no cancelSurplus flag, since shedding the
// surplus once the K-quorum is reached is valuesched's own policy, not
// something the coordinator asks for. Its leaves reserve no mcpu (unlike
// every other arm's Mcpu=1000), so res.TotalCoreSeconds/WastedCoreSeconds
// read as 0 -- a different admission regime, not a faster one; makespan and
// NAttemptsStopped are what's comparable.
func runCodedMatMulValueProcsArm(t *testing.T, c *clnt.Clnt, cfg *codedmatmul.Config, want *mat.Dense, name string) *codedmatmul.Result {
	j, err := codedmatmul.StartValueProcsJob(c, cfg)
	if !assert.Nil(t, err, "%s: StartValueProcsJob err %v", name, err) {
		return nil
	}
	C, samples, stats, err := j.Wait()
	if !assert.Nil(t, err, "%s: Wait err %v", name, err) {
		return nil
	}
	if !assert.True(t, mat.EqualApprox(C, want, 1e-6), "%s: decoded C does not match the reference", name) {
		return nil
	}
	res := codedmatmul.Analyze(samples, 0, stats)
	stopped, err := j.NAttemptsStopped()
	if err != nil {
		db.DPrintf(db.ALWAYS, "%s: NAttemptsStopped err %v", name, err)
	}
	db.DPrintf(db.ALWAYS, "CodedMatMul %s: makespan %v, mcpu=0 (unreserved), %d attempts stopped (surplus reclaimed)",
		name, res.Makespan, stopped)
	if st, err := j.Status(); err == nil {
		db.DPrintf(db.ALWAYS, "CodedMatMul %s final tree: %s", name, dumpTreeStatus(st))
	} else {
		db.DPrintf(db.ALWAYS, "%s: Status err %v", name, err)
	}
	return res
}

func TestCodedMatMul(t *testing.T) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return
	}
	defer mrts.Shutdown()
	sc := mrts.GetRealm(REALM1).SigmaClnt

	vpjob := adapter.StartJob(sc, 0)
	defer vpjob.Stop()
	vpc := clnt.NewClnt(sc.FsLib)

	fillers := injectContention(t, sc)
	defer releaseContention(sc, fillers)

	cfg := codedmatmul.DefaultConfig()
	// DefaultConfig leaves Mem at 0 (unconstrained). Only declare a reservation
	// when contention injection is actually requested -- besched's memory-based
	// admission would otherwise apply unconditionally and can perturb this
	// test's timing assertions even with no filler procs running.
	if contentionEnabled() {
		cfg.Mem = ContentionWorkerMem
	}
	db.DPrintf(db.ALWAYS, "TestCodedMatMul: cfg M=%d D=%d W=%d N=%d K=%d Mcpu=%d Mem=%d Repeats=%d StragglerIdx=%v",
		cfg.M, cfg.D, cfg.W, cfg.N, cfg.K, cfg.Mcpu, cfg.Mem, cfg.Repeats, cfg.StragglerIdx)
	r := cfg.M / cfg.K

	// Reference C, computed once directly (not via the harness) from the
	// same deterministically-regenerated A/B every worker reconstructs.
	blocks, B := codedmatmul.GenBlocks(cfg.Seed, cfg.K, r, cfg.D, cfg.W)
	A := mat.NewDense(cfg.M, cfg.D, nil)
	for j, b := range blocks {
		A.Slice(j*r, (j+1)*r, 0, cfg.D).(*mat.Dense).Copy(b)
	}
	var want mat.Dense
	want.Mul(A, B)

	// Arm 1: uncoded barrier (N=K). The default straggler (worker 0) sets
	// the makespan since every one of the K workers is required.
	arm1Cfg := *cfg
	arm1Cfg.N = cfg.K
	arm1 := runCodedMatMulArm(t, sc, &arm1Cfg, false, &want, "arm1-uncoded-barrier")

	// Arm 2: coded, no cancel (N=K+m). Surplus workers, including any that
	// finish after the quorum, run to completion.
	arm2 := runCodedMatMulArm(t, sc, cfg, false, &want, "arm2-coded-no-cancel")

	// Arm 3: coded + reap (N=K+m). Surplus workers are evicted as soon as
	// the K-quorum is reached.
	arm3 := runCodedMatMulArm(t, sc, cfg, true, &want, "arm3-coded-reap")

	// Arm 4: coded, scheduled by valuesched (N=K+m). Shedding the surplus
	// once the quorum is reached is valuesched's own policy rather than an
	// explicit cancelSurplus ask.
	arm4 := runCodedMatMulValueProcsArm(t, vpc, cfg, &want, "arm4-valueprocs")

	if arm1 == nil || arm2 == nil || arm3 == nil || arm4 == nil {
		return
	}

	assert.True(t, arm3.WastedCoreSeconds < arm2.WastedCoreSeconds,
		"Arm 3 (reap) should waste less compute than Arm 2 (no cancel): %.2f vs %.2f core-s",
		arm3.WastedCoreSeconds, arm2.WastedCoreSeconds)
	assert.True(t, arm3.Makespan < arm1.Makespan,
		"Arm 3 (reap) should finish faster than Arm 1 (uncoded barrier) under the default straggler: %v vs %v",
		arm3.Makespan, arm1.Makespan)

	// Informational only: arm4 reserves no mcpu (a different admission
	// regime, see runCodedMatMulValueProcsArm), so its makespan is not
	// asserted against arm1/arm3, only logged alongside them.
	db.DPrintf(db.ALWAYS, "CodedMatMul comparison: arm1 %v, arm3 %v, arm4 %v",
		arm1.Makespan, arm3.Makespan, arm4.Makespan)
}
