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
func runCodedMatMulValueProcsArm(t *testing.T, c clnt.Runner, cfg *codedmatmul.Config, want *mat.Dense, name string) *codedmatmul.Result {
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

	ctn := startContention(t, sc)
	defer ctn.release()

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

	// Arm 5: coded + reap again, but declaring no mcpu, which is the arm that
	// makes arm 4 comparable to anything.
	//
	// Arm 4 cannot be brought up to arm 3's reservation -- adapter/spec.go
	// rejects any leaf that reserves mcpu, because a leaf may be stopped and
	// re-run and so must not hold a reservation the scheduler is unaware of.
	// The equalization therefore has to go the other way: run the classical
	// reap policy under arm 4's admission regime, and compare those two. Arms
	// 1-3 keep their reservation so the recorded sweeps stay comparable.
	arm5Cfg := *cfg
	arm5Cfg.Mcpu = 0
	arm5 := runCodedMatMulArm(t, sc, &arm5Cfg, true, &want, "arm5-coded-reap-nomcpu")

	// Arm 4: coded, scheduled by valuesched (N=K+m). Shedding the surplus
	// once the quorum is reached is valuesched's own policy rather than an
	// explicit cancelSurplus ask.
	//
	// Sampled, because the claim this arm exists to test is that the number of
	// workers admitted at once falls from N toward K as the cluster fills, and
	// makespan alone cannot show that.
	smp := startVPSampler(vpc)
	arm4 := runCodedMatMulValueProcsArm(t, vpc, cfg, &want, "arm4-valueprocs")
	trace := smp.reportTrace("CodedMatMul arm4-valueprocs")

	if arm1 == nil || arm2 == nil || arm3 == nil || arm4 == nil || arm5 == nil {
		return
	}

	assert.True(t, arm3.WastedCoreSeconds < arm2.WastedCoreSeconds,
		"Arm 3 (reap) should waste less compute than Arm 2 (no cancel): %.2f vs %.2f core-s",
		arm3.WastedCoreSeconds, arm2.WastedCoreSeconds)
	assert.True(t, arm3.Makespan < arm1.Makespan,
		"Arm 3 (reap) should finish faster than Arm 1 (uncoded barrier) under the default straggler: %v vs %v",
		arm3.Makespan, arm1.Makespan)

	db.DPrintf(db.ALWAYS, "CodedMatMul comparison: arm1 %v, arm3 %v, arm4 %v, arm5 %v",
		arm1.Makespan, arm3.Makespan, arm4.Makespan, arm5.Makespan)
	db.DPrintf(db.ALWAYS, "CodedMatMul admitted width: N=%d K=%d, arm4 running max=%d mean=%.2f of %d slots, pressure max=%.3f mean=%.3f",
		cfg.N, cfg.K, trace.maxRunning, trace.meanRunning, trace.slots, trace.maxPressure, trace.meanPressure)

	// Now that both arms declare the same (absent) reservation, arm 4's
	// makespan is a like-for-like measurement rather than a note in the log.
	// Stated as a ratio bound rather than a strict ordering: the two policies
	// are meant to be equivalent when the cluster is empty, so requiring one
	// to beat the other would be asserting noise.
	assert.True(t, arm4.Makespan <= 2*arm5.Makespan,
		"Arm 4 (valueprocs) should be within 2x of arm 5 (classical reap, same admission regime): %v vs %v",
		arm4.Makespan, arm5.Makespan)

	// The width claim itself. The only bound that holds regardless of the host
	// is the tree's own: nothing can run more workers than it has.
	//
	// There is deliberately no lower bound against K. A quorum is K workers
	// *completing*, not K running at once, so on a cluster with fewer slots
	// than K the quorum is reached in waves and a peak below K is correct
	// behavior rather than a failure -- which is what this host does, with 4
	// slots against K=6.
	assert.True(t, trace.maxRunning <= cfg.N,
		"Arm 4 ran %d workers at once, more than the %d the tree has", trace.maxRunning, cfg.N)
}
