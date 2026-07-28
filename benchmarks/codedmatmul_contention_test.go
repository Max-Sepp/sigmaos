package benchmarks_test

// TestCodedMatMulSlotContention re-runs the same three-arm codedmatmul
// comparison as TestCodedMatMul (see codedmatmul_test.go), but first occupies
// most of the host's memory with filler `sleeper` procs so only a handful of
// worker-sized memory slots are left free -- fewer than K. This creates
// genuine SigmaOS scheduler-level (besched) queueing: besched's isEligible
// check (sched/besched/srv/srv.go) admits a BE proc only if its declared Mem
// fits in the requesting msched's free memory, so with fewer free slots than
// K, even Arm 1 (uncoded, needs all K workers) can't run all of them at once.

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"gonum.org/v1/gonum/mat"

	"sigmaos/apps/codedmatmul"
	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/sigmaclnt"
	sp "sigmaos/sigmap"
	"sigmaos/test"
	"sigmaos/util/linux/mem"
)

const (
	// ContentionWorkerMem is each codedmatmul worker's declared memory
	// reservation for this benchmark -- a bit above its real ~150MB working
	// set (see apps/codedmatmul/worker.go), not artificially inflated, since
	// the filler procs below are what create the scarcity.
	ContentionWorkerMem = proc.Tmem(512)
	// ContentionK is the quorum size. Chosen so slot contention shows up
	// within the required K workers themselves, not just the surplus.
	ContentionK = 8
	// ContentionN is the total worker count for the coded arms (surplus = 3,
	// same ratio as the DefaultConfig()'s N=K+3).
	ContentionN = 11
	// ContentionRemainingSlots is how many worker-sized memory slots are left
	// free after filler procs are spawned -- deliberately fewer than
	// ContentionK, so even the bare quorum (no redundancy) must wait for slot
	// turnover.
	ContentionRemainingSlots = 5
	// ContentionKernelReserveMem is memory left unclaimed by fillers, as a
	// buffer for kernel services (named, msched, etc) already running on the
	// host.
	ContentionKernelReserveMem = proc.Tmem(1000)
	// ContentionFillerChunkMem is the declared memory size of each filler
	// sleeper proc.
	ContentionFillerChunkMem = proc.Tmem(1000)
	// ContentionFillerSleep is comfortably longer than the benchmark run.
	ContentionFillerSleep = 600 * time.Second
)

// spawnFillerProcs spawns enough filler `sleeper` procs (each just sleeps --
// no real memory touched) to consume most of the host's memory as far as
// besched's memory-based admission accounting is concerned, leaving room for
// only ContentionRemainingSlots worker-sized slots. Returns the spawned procs
// so the caller can evict/reap them afterward.
func spawnFillerProcs(t *testing.T, sc *sigmaclnt.SigmaClnt) []*proc.Proc {
	total := mem.GetTotalMem()
	remaining := proc.Tmem(ContentionRemainingSlots) * ContentionWorkerMem
	if !assert.True(t, total > remaining+ContentionKernelReserveMem,
		"Host memory (%vMB) too small for this benchmark's filler sizing", total) {
		return nil
	}
	budget := total - remaining - ContentionKernelReserveMem
	nFillers := int(budget / ContentionFillerChunkMem)

	procs := make([]*proc.Proc, 0, nFillers)
	for i := 0; i < nFillers; i++ {
		p := proc.NewProc("sleeper", []string{fmt.Sprintf("%v", ContentionFillerSleep), "name/"})
		p.SetMem(ContentionFillerChunkMem)
		if !assert.Nil(t, sc.Spawn(p), "Err Spawn filler proc") {
			continue
		}
		if !assert.Nil(t, sc.WaitStart(p.GetPid()), "Err WaitStart filler proc") {
			continue
		}
		procs = append(procs, p)
	}
	db.DPrintf(db.ALWAYS, "CodedMatMul slot contention: total mem %vMB, spawned %d filler procs (%vMB each), leaving ~%d worker slots free",
		total, len(procs), ContentionFillerChunkMem, ContentionRemainingSlots)
	return procs
}

func evictFillerProcs(sc *sigmaclnt.SigmaClnt, procs []*proc.Proc) {
	for _, p := range procs {
		sc.Evict(p.GetPid())
	}
	for _, p := range procs {
		sc.WaitExit(p.GetPid())
	}
}

func TestCodedMatMulSlotContention(t *testing.T) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return
	}
	defer mrts.Shutdown()
	sc := mrts.GetRealm(REALM1).SigmaClnt

	fillers := spawnFillerProcs(t, sc)
	defer evictFillerProcs(sc, fillers)

	cfg := &codedmatmul.Config{
		M: ContentionK * 64, D: 65536, W: 64,
		N: ContentionN, K: ContentionK,
		Repeats:      4,
		StragglerIdx: []int{0},
		Tiles:        8,
		Mcpu:         1000,
		Mem:          ContentionWorkerMem,
		Seed:         7159623,
	}
	r := cfg.M / cfg.K

	// Reference C, computed once directly (not via the harness), same
	// approach as TestCodedMatMul.
	blocks, B := codedmatmul.GenBlocks(cfg.Seed, cfg.K, r, cfg.D, cfg.W)
	A := mat.NewDense(cfg.M, cfg.D, nil)
	for j, b := range blocks {
		A.Slice(j*r, (j+1)*r, 0, cfg.D).(*mat.Dense).Copy(b)
	}
	var want mat.Dense
	want.Mul(A, B)

	// Arm 1: uncoded barrier (N=K). With fewer free memory slots than K, not
	// all K required workers can run concurrently -- some must wait for a
	// slot to free up before the quorum can even be attempted.
	arm1Cfg := *cfg
	arm1Cfg.N = cfg.K
	arm1 := runCodedMatMulArm(t, sc, &arm1Cfg, false, &want, "arm1-uncoded-barrier")

	// Arm 2: coded, no cancel (N=K+m).
	arm2 := runCodedMatMulArm(t, sc, cfg, false, &want, "arm2-coded-no-cancel")

	// Arm 3: coded + reap (N=K+m).
	arm3 := runCodedMatMulArm(t, sc, cfg, true, &want, "arm3-coded-reap")

	if arm1 == nil || arm2 == nil || arm3 == nil {
		return
	}

	db.DPrintf(db.ALWAYS, "CodedMatMul slot contention: arm1 makespan %v, arm2 makespan %v, arm3 makespan %v",
		arm1.Makespan, arm2.Makespan, arm3.Makespan)
}
