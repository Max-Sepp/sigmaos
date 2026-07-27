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

func TestCodedMatMul(t *testing.T) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return
	}
	defer mrts.Shutdown()
	sc := mrts.GetRealm(REALM1).SigmaClnt

	cfg := codedmatmul.DefaultConfig()
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

	if arm1 == nil || arm2 == nil || arm3 == nil {
		return
	}

	assert.True(t, arm3.WastedCoreSeconds < arm2.WastedCoreSeconds,
		"Arm 3 (reap) should waste less compute than Arm 2 (no cancel): %.2f vs %.2f core-s",
		arm3.WastedCoreSeconds, arm2.WastedCoreSeconds)
	assert.True(t, arm3.Makespan < arm1.Makespan,
		"Arm 3 (reap) should finish faster than Arm 1 (uncoded barrier) under the default straggler: %v vs %v",
		arm3.Makespan, arm1.Makespan)
}
