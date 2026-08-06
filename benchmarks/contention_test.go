package benchmarks_test

// Shared contention injection for the HPSearch/MR/CodedMatMul benchmarks.
//
// Two limbs, because the scheduler's pressure reading folds max(memP, cpuP)
// (valueprocs/adapter/probe.go) and a benchmark that only ever moves one of
// them leaves the other half of that fold untested:
//
//   - memory. Every one of those apps' workers reserves declared memory
//     (proc.Tmem), so occupying the rest of the host's memory with filler
//     `sleeper` procs creates genuine besched-level queueing (see
//     sched/besched/srv/srv.go's isEligible) without needing app-specific
//     contention logic in each benchmark. The fillers touch no real memory;
//     they compete purely on the declared reservation besched admits against.
//   - cpu. Filler `spinner` procs, which burn a core each until evicted, move
//     the utilization the probe actually samples rather than a declared
//     reservation. Nothing queues behind them at admission; they make the
//     cluster slow rather than closed, which is the other way of being full.
//
// And three shapes, because the squeeze the applications are meant to survive
// arrives rather than being there from the start. A run contended from t=0
// never contains an arrival, so it cannot distinguish an engine that responds
// to a change in pressure from one that responds only to its level, and it
// never exercises the dwell and hysteresis that exist to damp that response.
//
//	constant  fillers up before the job starts, down after it ends (the
//	          original behavior, and still the default so recorded sweeps stay
//	          comparable)
//	step      slack until -contention_onset, squeezed for the rest of the run
//	pulse     slack, squeezed at -contention_onset, slack again after
//	          -contention_lift: the application has to contract and re-expand
//	          within one job
//
// Onset and lift are absolute durations rather than fractions of the run,
// since nothing here knows how long the job it is wrapping will take.

import (
	"flag"
	"fmt"
	"sync"
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

// The -contention_shape values.
const (
	ShapeConstant = "constant"
	ShapeStep     = "step"
	ShapePulse    = "pulse"
)

var (
	// contentionFreeMB is how many MB of host memory the injector should
	// leave free (as far as besched's memory-based admission accounting is
	// concerned), by occupying the rest with filler procs. Negative (the
	// default) disables memory contention, so every benchmark that calls
	// startContention runs uncontended unless this flag is set.
	contentionFreeMB int
	// contentionCPUProcs is how many core-burning `spinner` fillers to run
	// alongside the memory fillers. 0 (the default) disables the cpu limb.
	contentionCPUProcs int
	contentionShape    string
	contentionOnset    time.Duration
	contentionLift     time.Duration
)

func init() {
	flag.IntVar(&contentionFreeMB, "contention_free_mb", -1,
		"MB of host memory to leave free by spawning filler procs to consume the rest; negative disables memory contention injection")
	flag.IntVar(&contentionCPUProcs, "contention_cpu_procs", 0,
		"number of core-burning spinner procs to run as cpu contention; 0 disables the cpu limb")
	flag.StringVar(&contentionShape, "contention_shape", ShapeConstant,
		"when contention is applied: constant (before the job starts), step (at -contention_onset, held), or pulse (onset until -contention_lift)")
	flag.DurationVar(&contentionOnset, "contention_onset", 10*time.Second,
		"for -contention_shape=step/pulse, how long after the job starts before contention is injected")
	flag.DurationVar(&contentionLift, "contention_lift", 10*time.Second,
		"for -contention_shape=pulse, how long the squeeze is held before it is released")
}

// contentionEnabled reports whether memory contention was requested. Callers
// that need to give their workers a declared Mem reservation for contention
// to have any effect (besched's memory-based admission only applies to
// procs that declare Mem > 0) should gate that on this, rather than always
// declaring one -- an always-declared Mem changes the app's admission
// regime even when contention injection is disabled, which can perturb
// timing assertions that were tuned against the unconstrained default.
//
// Deliberately not true for cpu-only contention: a spinner competes for
// cycles, not for the memory besched admits against, so declaring a
// reservation would change the admission regime without making the cpu
// fillers any more effective.
func contentionEnabled() bool {
	return contentionFreeMB >= 0
}

// cpuContentionEnabled reports whether the cpu limb was requested.
func cpuContentionEnabled() bool {
	return contentionCPUProcs > 0
}

// anyContentionEnabled reports whether either limb was requested.
func anyContentionEnabled() bool {
	return contentionEnabled() || cpuContentionEnabled()
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

// injectContentionCPU spawns n `spinner` fillers, each of which burns a core
// until evicted.
//
// Each declares a full core so besched charges the cluster for it, which is
// what makes the load visible to the scheduler's occupancy accounting as well
// as to the probe's sampled cpu utilization. WaitStart matters more here than
// for the memory fillers: a spinner that has not started yet is burning
// nothing, so returning before they are all up would report a squeeze that
// has not happened.
func injectContentionCPU(t *testing.T, sc *sigmaclnt.SigmaClnt, n int) []*proc.Proc {
	procs := make([]*proc.Proc, 0, n)
	for i := 0; i < n; i++ {
		p := proc.NewProc("spinner", []string{"name/"})
		p.SetMcpu(1000)
		if !assert.Nil(t, sc.Spawn(p), "Err Spawn cpu filler proc") {
			continue
		}
		if !assert.Nil(t, sc.WaitStart(p.GetPid()), "Err WaitStart cpu filler proc") {
			continue
		}
		procs = append(procs, p)
	}
	db.DPrintf(db.ALWAYS, "injectContention: spawned %d cpu filler procs (1 core each)", len(procs))
	return procs
}

// releaseContention evicts and reaps filler procs. Safe to call with a
// nil/empty slice.
func releaseContention(sc *sigmaclnt.SigmaClnt, procs []*proc.Proc) {
	for _, p := range procs {
		sc.Evict(p.GetPid())
	}
	for _, p := range procs {
		sc.WaitExit(p.GetPid())
	}
}

// contention is a running contention schedule. Every benchmark holds one for
// the length of its job and releases it at the end, whatever shape was asked
// for -- so a test body reads the same regardless of when the squeeze lands.
type contention struct {
	t  *testing.T
	sc *sigmaclnt.SigmaClnt

	mu    sync.Mutex
	procs []*proc.Proc

	stop chan struct{}
	done chan struct{}
}

// startContention begins the schedule selected by -contention_shape and
// returns a handle the caller must release.
//
// For the constant shape the fillers are up before this returns, which is
// what keeps a default run byte-for-byte the experiment the recorded sweeps
// measured. The other two shapes return immediately and inject later, so the
// clock the onset is measured from is the moment the caller starts its job.
func startContention(t *testing.T, sc *sigmaclnt.SigmaClnt) *contention {
	c := &contention{t: t, sc: sc, stop: make(chan struct{}), done: make(chan struct{})}
	if !anyContentionEnabled() {
		db.DPrintf(db.ALWAYS, "startContention: disabled (neither -contention_free_mb nor -contention_cpu_procs set), running uncontended")
		close(c.done)
		return c
	}
	switch contentionShape {
	case ShapeConstant:
		c.inject()
		close(c.done)
	case ShapeStep, ShapePulse:
		db.DPrintf(db.ALWAYS, "startContention: shape=%v onset=%v lift=%v (job starts uncontended)",
			contentionShape, contentionOnset, contentionLift)
		go c.schedule()
	default:
		assert.Fail(t, "unknown -contention_shape", "%v (want %v, %v or %v)",
			contentionShape, ShapeConstant, ShapeStep, ShapePulse)
		close(c.done)
	}
	return c
}

// squeezeAfter is a step schedule built programmatically rather than from the
// flags, for the tests whose whole point is that the squeeze arrives partway
// through -- they must apply one whatever -contention_shape the sweep asked
// for, or they would silently degrade into another constant-contention run.
func squeezeAfter(t *testing.T, sc *sigmaclnt.SigmaClnt, freeMB proc.Tmem, delay time.Duration) *contention {
	c := &contention{t: t, sc: sc, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(c.done)
		select {
		case <-time.After(delay):
		case <-c.stop:
			return
		}
		db.DPrintf(db.ALWAYS, "contention: programmatic squeeze to %vMB free at +%v", freeMB, delay)
		procs := injectContentionFreeMB(c.t, c.sc, freeMB)
		c.mu.Lock()
		c.procs = append(c.procs, procs...)
		c.mu.Unlock()
	}()
	return c
}

// schedule runs the step and pulse shapes. Both wait out the onset; only
// pulse lifts the squeeze again. Either wait can be cut short by release, so
// a job that finishes before its own contention was due does not hold the
// test open waiting to inject something nobody will observe.
func (c *contention) schedule() {
	defer close(c.done)
	select {
	case <-time.After(contentionOnset):
	case <-c.stop:
		return
	}
	db.DPrintf(db.ALWAYS, "contention: onset at +%v", contentionOnset)
	c.inject()
	if contentionShape != ShapePulse {
		return
	}
	select {
	case <-time.After(contentionLift):
	case <-c.stop:
		return
	}
	db.DPrintf(db.ALWAYS, "contention: lift at +%v", contentionOnset+contentionLift)
	c.clear()
}

// inject spawns both limbs, whichever were asked for.
func (c *contention) inject() {
	var procs []*proc.Proc
	if contentionEnabled() {
		procs = append(procs, injectContentionFreeMB(c.t, c.sc, proc.Tmem(contentionFreeMB))...)
	}
	if cpuContentionEnabled() {
		procs = append(procs, injectContentionCPU(c.t, c.sc, contentionCPUProcs)...)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.procs = append(c.procs, procs...)
}

// clear evicts whatever is currently injected, leaving the handle reusable.
func (c *contention) clear() {
	c.mu.Lock()
	procs := c.procs
	c.procs = nil
	c.mu.Unlock()
	releaseContention(c.sc, procs)
}

// release ends the schedule and evicts every filler still running. Safe to
// call more than once, and safe on a handle whose shape never injected
// anything.
func (c *contention) release() {
	select {
	case <-c.stop:
	default:
		close(c.stop)
	}
	<-c.done
	c.clear()
}
