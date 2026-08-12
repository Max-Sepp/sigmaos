package benchmarks_test

// Shared contention injection for the HPSearch/MR/CodedMatMul benchmarks.
//
// Two limbs, because the scheduler's pressure reading folds max(memP, cpuP)
// (valueprocs/adapter/probe.go) and a benchmark that only ever moves one of
// them leaves the other half of that fold untested:
//
//   - memory. `memfiller` procs occupy real resident memory until only
//     -contention_free_mb is left, and hold it. The load is real rather than
//     declared because besched admits on declared Mem, so a filler that only
//     declares creates scarcity for procs that also declare and none at all
//     for procs that do not -- and the applications under test declare
//     nothing.
//   - cpu. Filler `spinner` procs, which burn a core each until evicted, move
//     the utilization the probe samples. Nothing queues behind them at
//     admission; they make the cluster slow rather than closed, which is the
//     other way of being full.
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
//
// # Safety
//
// Real memory pressure can take the host into the OOM killer, which on a
// machine running SigmaOS is as likely to claim etcd or named as a filler and
// end the run by ending the instance. Four independent layers stand between a
// requested level and that outcome, so that no single failure reaches it:
//
//  1. the harness refuses the run outright if the level is unsafe for this
//     host, or if the host cannot deliver it (see preflightContention)
//  2. each filler ramps in chunks against a fresh MemAvailable reading, and
//     sheds if it ever drops below the abort floor
//  3. each filler holds a TTL, so a test that dies without evicting cannot
//     pin memory indefinitely
//  4. each filler raises its own oom_score_adj, so a kernel that does run the
//     OOM killer takes a filler first
//
// Layer 1 lives here; the rest live in cmd/user/memfiller.

import (
	"flag"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	db "sigmaos/debug"
	"sigmaos/proc"
	mschedclnt "sigmaos/sched/msched/clnt"
	"sigmaos/sigmaclnt"
	sp "sigmaos/sigmap"
	"sigmaos/util/linux/mem"
)

const (
	// ContentionFillerBin holds real memory, unlike a `sleeper` with a
	// declared reservation.
	ContentionFillerBin = "memfiller"
	// AbsMinFreeMB is the least memory any level may ask to leave free. Below
	// this the kernel services themselves have no room to work, and what the
	// run measures is the host dying rather than the scheduler deciding.
	//
	// This is the policy; cmd/user/memfiller carries a lower clamp of its own
	// as a last resort for a filler spawned by something other than this
	// harness.
	AbsMinFreeMB = 2048
	// ContentionDeliveryTolMB is how far the achieved free level may sit from
	// the requested one before the run is refused. Some slack is unavoidable:
	// the fillers stop at chunk granularity and the machine keeps moving
	// underneath them.
	ContentionDeliveryTolMB = 512
	// ContentionReleaseTimeout bounds the wait for one filler to exit, so a
	// wedged filler costs seconds rather than the whole go-test timeout.
	ContentionReleaseTimeout = 30 * time.Second
	// ContentionWatchPeriod is how often the harness re-reads real memory.
	ContentionWatchPeriod = 500 * time.Millisecond
)

// The -contention_shape values.
const (
	ShapeConstant = "constant"
	ShapeStep     = "step"
	ShapePulse    = "pulse"
)

var (
	// contentionFreeMB is how many MB of real memory to leave free, by
	// occupying the rest. Negative (the default) disables memory contention,
	// so every benchmark that calls startContention runs uncontended unless
	// this flag is set.
	//
	// "Leave N free" rather than "hold N" because N is the headroom the
	// application gets, which is what an experiment holds fixed. How much has
	// to be occupied to reach it depends on the host and on what else is
	// resident, so a level expressed that way would not be the same level
	// twice.
	contentionFreeMB int
	// contentionAbortFloorMB is where the fillers start giving memory back.
	// Well below contentionFreeMB: it is not the squeeze, it is the line
	// before the OOM killer.
	contentionAbortFloorMB int
	contentionChunkMB      int
	contentionTTL          time.Duration
	// contentionCPUProcs is how many core-burning `spinner` fillers to run
	// alongside the memory fillers. 0 (the default) disables the cpu limb.
	contentionCPUProcs int
	contentionShape    string
	contentionOnset    time.Duration
	contentionLift     time.Duration
)

func init() {
	flag.IntVar(&contentionFreeMB, "contention_free_mb", -1,
		"MB of real host memory to leave free by occupying the rest with filler procs; negative disables memory contention injection")
	flag.IntVar(&contentionAbortFloorMB, "contention_abort_floor_mb", 1024,
		"MB of real host memory below which fillers release, to keep the host clear of the OOM killer")
	flag.IntVar(&contentionChunkMB, "contention_chunk_mb", 256,
		"MB each filler allocates per step while ramping")
	flag.DurationVar(&contentionTTL, "contention_ttl", 15*time.Minute,
		"how long a filler holds its memory before releasing it unasked, so a died test cannot pin memory")
	flag.IntVar(&contentionCPUProcs, "contention_cpu_procs", 0,
		"number of core-burning spinner procs to run as cpu contention; 0 disables the cpu limb")
	flag.StringVar(&contentionShape, "contention_shape", ShapeConstant,
		"when contention is applied: constant (before the job starts), step (at -contention_onset, held), or pulse (onset until -contention_lift)")
	flag.DurationVar(&contentionOnset, "contention_onset", 10*time.Second,
		"for -contention_shape=step/pulse, how long after the job starts before contention is injected")
	flag.DurationVar(&contentionLift, "contention_lift", 10*time.Second,
		"for -contention_shape=pulse, how long the squeeze is held before it is released")
}

// contentionEnabled reports whether memory contention was requested.
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

// preflightContention refuses the run rather than delivering a squeeze that
// is unsafe or that is not the one the level names.
//
// Refusing beats clamping here. A run that quietly filled less than it was
// asked to would be recorded under a label describing pressure it never
// applied, and nothing downstream could tell it apart from one that did.
func preflightContention(t *testing.T) {
	if swap := mem.GetSwapTotal(); swap > 0 {
		t.Fatalf("contention: refusing, swap is on (%vMB). Fillers would be paged out, so MemAvailable would stop meaning what the level claims. Disable swap or run without -contention_free_mb", swap)
	}
	if contentionFreeMB < AbsMinFreeMB {
		t.Fatalf("contention: refusing, -contention_free_mb=%v is below the %vMB the kernel services need", contentionFreeMB, AbsMinFreeMB)
	}
	if contentionAbortFloorMB >= contentionFreeMB {
		t.Fatalf("contention: refusing, -contention_abort_floor_mb=%v is not below -contention_free_mb=%v, so the fillers would shed everything they took", contentionAbortFloorMB, contentionFreeMB)
	}
	if avail := int(mem.GetAvailableMem()); avail <= contentionFreeMB {
		t.Fatalf("contention: refusing, only %vMB is available and the level asks to leave %vMB free; the host is already more contended than the run claims to be", avail, contentionFreeMB)
	}
}

// fillerTargets is the kernel ID of every machine the fillers should squeeze.
func fillerTargets(sc *sigmaclnt.SigmaClnt) ([]string, error) {
	loads, err := mschedclnt.NewMSchedClnt(sc.FsLib, sp.NOT_SET).MSchedLoad()
	if err != nil {
		return nil, err
	}
	kids := make([]string, 0, len(loads))
	for kid := range loads {
		kids = append(kids, kid)
	}
	return kids, nil
}

// realAvailMB is the fleet's real available memory, summed.
func realAvailMB(sc *sigmaclnt.SigmaClnt) int {
	loads, err := mschedclnt.NewMSchedClnt(sc.FsLib, sp.NOT_SET).MSchedLoad()
	if err != nil {
		db.DPrintf(db.ALWAYS, "contention: MSchedLoad err %v, falling back to the local reading", err)
		return int(mem.GetAvailableMem())
	}
	total := 0
	for _, l := range loads {
		if !l.GetMemAvailValid() {
			// An msched that does not sample availability cannot be measured
			// this way; the local reading is the only honest answer for it.
			return int(mem.GetAvailableMem())
		}
		total += int(l.GetMemAvailMB())
	}
	return total
}

// injectContentionMem puts one filler on every machine and waits until the
// memory is resident.
//
// The fillers declare no reservation of their own: the load is real, so
// declaring one would put them back in the admission ledger the applications
// are no longer in, and change what the applications compete against.
// Errorf rather than Fatalf throughout: the step and pulse shapes inject from
// their own goroutine, and Fatalf outside the test's goroutine exits that
// goroutine instead of failing the test.
func injectContentionMem(t *testing.T, sc *sigmaclnt.SigmaClnt) []*proc.Proc {
	kids, err := fillerTargets(sc)
	if err != nil || len(kids) == 0 {
		t.Errorf("contention: cannot list machines to squeeze: %v (%d found)", err, len(kids))
		return nil
	}

	procs := make([]*proc.Proc, 0, len(kids))
	for _, kid := range kids {
		p := proc.NewProc(ContentionFillerBin, []string{
			fmt.Sprintf("%v", contentionFreeMB),
			fmt.Sprintf("%v", contentionAbortFloorMB),
			fmt.Sprintf("%v", contentionChunkMB),
			contentionTTL.String(),
			ContentionWatchPeriod.String(),
		})
		// Pinned so each machine gets exactly one filler; otherwise besched
		// is free to put every filler on one machine and leave the rest
		// unsqueezed.
		p.SetKernels([]string{kid})
		if !assert.Nil(t, sc.Spawn(p), "Err Spawn filler proc on %v", kid) {
			continue
		}
		if !assert.Nil(t, sc.WaitStart(p.GetPid()), "Err WaitStart filler proc on %v", kid) {
			continue
		}
		procs = append(procs, p)
	}
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
//
// Each wait is bounded: a filler that will not exit would otherwise hold the
// test open to the go-test timeout, and the filler's own TTL already puts a
// ceiling on how long it can keep the memory after that.
func releaseContention(sc *sigmaclnt.SigmaClnt, procs []*proc.Proc) {
	for _, p := range procs {
		sc.Evict(p.GetPid())
	}
	for _, p := range procs {
		done := make(chan struct{})
		go func(pid sp.Tpid) {
			sc.WaitExit(pid)
			close(done)
		}(p.GetPid())
		select {
		case <-done:
		case <-time.After(ContentionReleaseTimeout):
			db.DPrintf(db.ALWAYS, "contention: filler %v did not exit within %v; its TTL will release it", p.GetPid(), ContentionReleaseTimeout)
		}
	}
}

// contention is a running contention schedule. Every benchmark holds one for
// the length of its job and releases it at the end, whatever shape was asked
// for -- so a test body reads the same regardless of when the squeeze lands.
type contention struct {
	t  *testing.T
	sc *sigmaclnt.SigmaClnt

	// startedAt is the clock the onset and lift are measured from, so anything
	// else watching the run can line its own trace up against the schedule.
	startedAt time.Time

	mu    sync.Mutex
	procs []*proc.Proc

	// aborted records that the watchdog had to take the squeeze away. The run
	// is no longer at the level it is labelled with, so it is a failure rather
	// than a weaker data point.
	aborted bool

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
	c := &contention{t: t, sc: sc, startedAt: time.Now(), stop: make(chan struct{}), done: make(chan struct{})}
	if !anyContentionEnabled() {
		db.DPrintf(db.ALWAYS, "startContention: disabled (neither -contention_free_mb nor -contention_cpu_procs set), running uncontended")
		close(c.done)
		return c
	}
	if contentionEnabled() {
		preflightContention(t)
		go c.watch()
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

// schedule runs the step and pulse shapes. Both wait out the onset; only
// pulse lifts the squeeze again. Either wait can be cut short by release, so
// a job that finishes before its own contention was due does not hold the
// test open waiting to inject something nobody will observe.
func (c *contention) schedule() {
	defer close(c.done)
	select {
	case <-time.After(contentionOnset):
	case <-c.stop:
		// The job finished before its own squeeze was due, so this run is
		// uncontended whatever its level says. Said out loud because the
		// alternative is a cell labelled with pressure it never saw.
		db.DPrintf(db.ALWAYS, "contention: NEVER APPLIED, the job finished before the %v onset; this run is uncontended despite shape=%v free_mb=%v",
			contentionOnset, contentionShape, contentionFreeMB)
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
		db.DPrintf(db.ALWAYS, "contention: job finished before the +%v lift, so the squeeze was never released within the run",
			contentionOnset+contentionLift)
		return
	}
	db.DPrintf(db.ALWAYS, "contention: lift at +%v", contentionOnset+contentionLift)
	c.clear()
}

// watch takes the squeeze away if the host gets closer to the OOM killer than
// the abort floor allows, and fails the run for having done so.
//
// Two readings, because they fail differently. The per-machine one is the
// only one that sees a multi-machine cluster; the local one costs no RPC and
// still works when msched is wedged, which is exactly when the RPC reading is
// least trustworthy and most likely to matter.
func (c *contention) watch() {
	t := time.NewTicker(ContentionWatchPeriod)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
			local := int(mem.GetAvailableMem())
			fleet := realAvailMB(c.sc)
			if local >= contentionAbortFloorMB && fleet >= contentionAbortFloorMB {
				continue
			}
			db.DPrintf(db.ALWAYS, "contention: ABORT, available memory (local %vMB, fleet %vMB) fell below the abort floor %vMB; releasing the squeeze",
				local, fleet, contentionAbortFloorMB)
			c.mu.Lock()
			c.aborted = true
			c.mu.Unlock()
			c.clear()
			// Errorf rather than Fatalf: this is not the test's goroutine, and
			// Fatalf from here would leave the benchmark running with its
			// result unreported.
			c.t.Errorf("contention: squeeze withdrawn to keep the host clear of the OOM killer; this run is not at the level it was asked for")
			return
		}
	}
}

// inject spawns both limbs, whichever were asked for.
func (c *contention) inject() {
	var procs []*proc.Proc
	before := 0
	if contentionEnabled() {
		before = realAvailMB(c.sc)
		procs = append(procs, injectContentionMem(c.t, c.sc)...)
	}
	if cpuContentionEnabled() {
		procs = append(procs, injectContentionCPU(c.t, c.sc, contentionCPUProcs)...)
	}
	c.mu.Lock()
	c.procs = append(c.procs, procs...)
	c.mu.Unlock()

	if !contentionEnabled() {
		return
	}
	// Measured, not claimed. The fillers report what they did, but what the
	// benchmark ran against is what the machine says is left.
	after := realAvailMB(c.sc)
	c.summarize(before, after)
	if d := after - contentionFreeMB; d > ContentionDeliveryTolMB || d < -ContentionDeliveryTolMB {
		c.t.Errorf("contention: asked to leave %vMB free but %vMB is available after filling (was %vMB); this run is not at its labelled level",
			contentionFreeMB, after, before)
	}
}

func (c *contention) summarize(beforeMB, afterMB int) {
	c.mu.Lock()
	aborted, n := c.aborted, len(c.procs)
	c.mu.Unlock()
	db.DPrintf(db.ALWAYS,
		"contention: shape=%v requested_free=%dMB achieved_free=%dMB occupied=%dMB abort_floor=%dMB fillers=%d cpu_fillers=%d aborted=%v",
		contentionShape, contentionFreeMB, afterMB, beforeMB-afterMB, contentionAbortFloorMB,
		n-contentionCPUProcs, contentionCPUProcs, aborted)
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
	if contentionEnabled() {
		db.DPrintf(db.ALWAYS, "contention: released, available now %vMB", realAvailMB(c.sc))
	}
}
