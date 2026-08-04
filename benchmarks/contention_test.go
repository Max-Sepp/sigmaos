package benchmarks_test

// Shared memory-contention injection for the HPSearch/MR/CodedMatMul
// benchmarks. Every one of those apps' workers reserves declared memory
// (proc.Tmem), so occupying the rest of the host's memory with filler
// `sleeper` procs creates genuine besched-level queueing (see
// sched/besched/srv/srv.go's isEligible) without needing app-specific
// contention logic in each benchmark.

import (
	"flag"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/sigmaclnt"
	"sigmaos/util/linux/mem"
)

const (
	// ContentionFillerChunkMem is the declared memory size of each filler
	// sleeper proc.
	ContentionFillerChunkMem = proc.Tmem(1000)
	// ContentionKernelReserveMem is memory left unclaimed by fillers, as a
	// buffer for kernel services (named, msched, etc) already running on the
	// host.
	ContentionKernelReserveMem = proc.Tmem(1000)
	// ContentionFillerSleep is comfortably longer than any benchmark run in
	// this package.
	ContentionFillerSleep = 600 * time.Second
)

// contentionFreeMB is how many MB of host memory injectContention should
// leave free (as far as besched's memory-based admission accounting is
// concerned), by occupying the rest with filler procs. Negative (the
// default) disables contention injection, so every benchmark that calls
// injectContention runs uncontended unless this flag is set.
var contentionFreeMB int

func init() {
	flag.IntVar(&contentionFreeMB, "contention_free_mb", -1,
		"MB of host memory to leave free by spawning filler procs to consume the rest; negative disables contention injection")
}

// injectContentionFreeMB spawns enough filler `sleeper` procs (each just
// sleeps -- no real memory touched) to consume the host's memory down to
// freeMB free. Returns the spawned procs so the caller can release them
// with releaseContention.
func injectContentionFreeMB(t *testing.T, sc *sigmaclnt.SigmaClnt, freeMB proc.Tmem) []*proc.Proc {
	total := mem.GetTotalMem()
	if !assert.True(t, total > freeMB+ContentionKernelReserveMem,
		"Host memory (%vMB) too small to leave %vMB free", total, freeMB) {
		return nil
	}
	budget := total - freeMB - ContentionKernelReserveMem
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
	db.DPrintf(db.ALWAYS, "injectContention: total mem %vMB, spawned %d filler procs (%vMB each), leaving ~%vMB free",
		total, len(procs), ContentionFillerChunkMem, freeMB)
	return procs
}

// injectContention is injectContentionFreeMB gated by -contention_free_mb --
// a no-op (returns nil) when the flag wasn't set, so callers can invoke it
// unconditionally at the top of a benchmark and get the uncontended case by
// default. Callers must still call releaseContention on the result, even
// when it's nil.
func injectContention(t *testing.T, sc *sigmaclnt.SigmaClnt) []*proc.Proc {
	if contentionFreeMB < 0 {
		return nil
	}
	return injectContentionFreeMB(t, sc, proc.Tmem(contentionFreeMB))
}

// releaseContention evicts and reaps the filler procs injectContention (or
// injectContentionFreeMB) spawned. Safe to call with a nil/empty slice.
func releaseContention(sc *sigmaclnt.SigmaClnt, procs []*proc.Proc) {
	for _, p := range procs {
		sc.Evict(p.GetPid())
	}
	for _, p := range procs {
		sc.WaitExit(p.GetPid())
	}
}
