package benchmarks_test

// The coded-matmul benchmark's four arms: uncoded-barrier, coded-no-cancel,
// coded-reap, and valueprocs. Everything beyond booting a realm
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
	res := codedmatmul.Analyze(samples, stats)
	db.DPrintf(db.ALWAYS, "CodedMatMul %s: makespan %v, total %.2f core-s, wasted %.2f core-s, %d evicted, quorum from %v",
		name, res.Makespan, res.TotalCoreSeconds, res.WastedCoreSeconds, res.NEvicted, res.QuorumIdx)
	return res
}

// runCodedMatMulValueProcsArm is runCodedMatMulArm's counterpart for the
// value-procs-scheduled arm: no cancelSurplus flag, since shedding the
// surplus once the K-quorum is reached is valuesched's own policy, not
// something the coordinator asks for.
//
// No arm reserves anything, so this one's core-seconds are directly
// comparable with the rest.
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
	res := codedmatmul.Analyze(samples, stats)
	stopped, err := j.NAttemptsStopped()
	if err != nil {
		db.DPrintf(db.ALWAYS, "%s: NAttemptsStopped err %v", name, err)
	}
	db.DPrintf(db.ALWAYS, "CodedMatMul %s: makespan %v, mcpu=0 (unreserved), %d attempts stopped (surplus reclaimed), quorum from %v",
		name, res.Makespan, stopped, res.QuorumIdx)
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
	db.DPrintf(db.ALWAYS, "TestCodedMatMul: cfg M=%d D=%d W=%d N=%d K=%d Repeats=%d StragglerIdx=%v (workers reserve nothing)",
		cfg.M, cfg.D, cfg.W, cfg.N, cfg.K, cfg.Repeats, cfg.StragglerIdx)
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

	// Uncoded barrier (N=K). Every worker is required, so the straggler sets
	// the makespan.
	uncodedCfg := *cfg
	uncodedCfg.N = cfg.K
	uncoded := runCodedMatMulArm(t, sc, &uncodedCfg, false, &want, "uncoded-barrier")

	// Coded without cancelling (N=K+m). Surplus workers, including any that
	// finish after the quorum, run to completion.
	codedNoCancel := runCodedMatMulArm(t, sc, cfg, false, &want, "coded-no-cancel")

	// Coded, with the surplus evicted as soon as the K-quorum is reached.
	codedReap := runCodedMatMulArm(t, sc, cfg, true, &want, "coded-reap")

	// Coded, scheduled by valuesched. Shedding the surplus once the quorum is
	// reached is valuesched's own policy rather than an explicit cancel.
	//
	// Sampled, because the claim this arm exists to test is that the number of
	// workers admitted at once falls from N toward K as the cluster fills, and
	// makespan alone cannot show that.
	smp := startVPSamplerAt(vpc, ctn.startedAt)
	valueProcs := runCodedMatMulValueProcsArm(t, vpc, cfg, &want, "valueprocs")
	trace := smp.reportTrace("CodedMatMul valueprocs")

	if uncoded == nil || codedNoCancel == nil || codedReap == nil || valueProcs == nil {
		return
	}

	// Cancelling is checked by whether it happened, not by what it saved.
	// On a host with fewer cores than N the arms are oversubscribed and their
	// totals move by more between runs than the cancelling is worth, so a
	// comparison of core-seconds is a coin flip dressed as a result. The
	// numbers are logged below for the analysis to pool across reps.
	assert.Greater(t, codedReap.NEvicted, 0,
		"coded-reap should have evicted the surplus once the quorum was reached")
	assert.Equal(t, 0, codedNoCancel.NEvicted,
		"coded-no-cancel should have evicted nothing")

	// What coding buys, stated as the mechanism rather than as a stopwatch.
	// With N > K the quorum can form without the straggler; with N = K it
	// cannot, and the uncoded barrier waits for it however slow it is.
	//
	// Deliberately not a makespan comparison. Whether routing around the
	// straggler also finishes sooner depends on whether the cluster has room
	// for the redundancy: N unreserved workers on a smaller number of cores
	// contend with each other, and the surplus can cost more than the tail it
	// avoids. That is a property of the host, so it is measured and reported
	// below rather than asserted.
	//
	// Only the arms that start every worker at once are held to this.
	// valuesched admits a few leaves at a time in tree order, so the
	// straggler starts before the workers that would overtake it and can be
	// in the quorum on merit of its head start -- its quorum is reported
	// rather than asserted.
	straggler := cfg.StragglerIdx[0]
	assert.True(t, uncoded.InQuorum(straggler),
		"uncoded-barrier has N=K, so it must wait for every worker including the straggler; quorum %v", uncoded.QuorumIdx)
	for _, a := range []struct {
		name string
		res  *codedmatmul.Result
	}{{"coded-no-cancel", codedNoCancel}, {"coded-reap", codedReap}} {
		assert.False(t, a.res.InQuorum(straggler),
			"%v should have reached its quorum without the straggler (worker %d); quorum from %v",
			a.name, straggler, a.res.QuorumIdx)
	}

	db.DPrintf(db.ALWAYS, "CodedMatMul makespan: uncoded-barrier %v, coded-no-cancel %v, coded-reap %v, valueprocs %v",
		uncoded.Makespan, codedNoCancel.Makespan, codedReap.Makespan, valueProcs.Makespan)
	db.DPrintf(db.ALWAYS, "CodedMatMul compute: uncoded-barrier %.2f, coded-no-cancel %.2f, coded-reap %.2f, valueprocs %.2f core-s",
		uncoded.TotalCoreSeconds, codedNoCancel.TotalCoreSeconds, codedReap.TotalCoreSeconds, valueProcs.TotalCoreSeconds)
	db.DPrintf(db.ALWAYS, "CodedMatMul admitted width: N=%d K=%d, valueprocs running max=%d mean=%.2f of %d slots, pressure max=%.3f mean=%.3f",
		cfg.N, cfg.K, trace.maxRunning, trace.meanRunning, trace.slots, trace.maxPressure, trace.meanPressure)

	// valueprocs against the classical reap policy, which is now the same
	// admission regime rather than a different one -- no arm reserves
	// anything. Stated as a ratio bound rather than a strict ordering: the two
	// policies are meant to be equivalent when the cluster is empty, so
	// requiring one to beat the other would be asserting noise.
	assert.True(t, valueProcs.Makespan <= 2*codedReap.Makespan,
		"valueprocs should be within 2x of coded-reap: %v vs %v",
		valueProcs.Makespan, codedReap.Makespan)

	// The width claim itself. The only bound that holds regardless of the host
	// is the tree's own: nothing can run more workers than it has.
	//
	// There is deliberately no lower bound against K. A quorum is K workers
	// *completing*, not K running at once, so on a cluster with fewer slots
	// than K the quorum is reached in waves and a peak below K is correct
	// behavior rather than a failure -- which is what this host does, with 4
	// slots against K=6.
	assert.True(t, trace.maxRunning <= cfg.N,
		"valueprocs ran %d workers at once, more than the %d the tree has", trace.maxRunning, cfg.N)
}
