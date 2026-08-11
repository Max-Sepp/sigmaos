// memfiller occupies real host memory until only a target amount is left
// free, and then holds it there for the length of a benchmark.
//
// The memory is genuinely resident, not a declared reservation. besched
// admits a proc by comparing its declared Mem against a ledger, so a filler
// that only declares creates scarcity for procs that also declare and none at
// all for procs that do not. Faulting the pages in makes the squeeze a fact
// about the machine, which every proc on it is subject to.
//
// Two things about the shape of it are deliberate.
//
// The target is "leave N MB free", not "hold X MB". N is the headroom the
// application under test gets, which is the quantity an experiment holds
// fixed; X depends on how much memory the host has and on whatever else is
// resident when the run starts, so a sweep expressed in X is not reproducible
// across machines or across days.
//
// And the hold is constant once it is reached: this proc does not re-target N
// as the application allocates. Doing so would hand the application its
// headroom back as fast as it consumed it, leaving no squeeze at all.
// MemAvailable falling below N as the run proceeds is the squeeze working.
// The watchdog serves a different purpose -- it sheds only against
// abortFloorMB, far below N, so a runaway application cannot take the host
// into the OOM killer.
package main

import (
	"os"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/sigmaclnt"
	"sigmaos/util/linux/mem"
)

const MB = 1 << 20

const (
	// AbsMinFreeMB is the least memory this proc will agree to leave free,
	// whatever it was asked for. A clamp rather than an error because it
	// backstops the harness's preflight rather than being where the policy
	// lives.
	AbsMinFreeMB = 1024
	// Chunk bounds. Small chunks make the ramp slow and the control loop
	// chatty; large ones let a single allocation overshoot the target by more
	// than the watchdog can see between polls.
	MinChunkMB = 64
	MaxChunkMB = 512
	// MaxTTL bounds how long a filler can outlive the test that spawned it.
	MaxTTL = 30 * time.Minute
	// MinPoll bounds how often the watchdog reads /proc/meminfo.
	MinPoll = 100 * time.Millisecond

	// pageStride is the granularity at which the filler touches its
	// allocation. One byte per page faults the page in, which is all
	// residency requires. Writing every byte would add nothing to it and cost
	// seconds of memory bandwidth at multi-GB sizes -- bandwidth that shows up
	// in the CPU signal a co-running contention limb is trying to measure.
	pageStride = 4096

	// noProgressLimit is how many consecutive chunks may fail to move
	// MemAvailable before the ramp gives up. A host that reclaims as fast as
	// this proc allocates would otherwise be filled until something died.
	noProgressLimit = 4
)

func main() {
	if len(os.Args) != 6 {
		db.DFatalf("Usage: %v targetFreeMB abortFloorMB chunkMB ttl pollInterval\nArgs: %v", os.Args[0], os.Args)
	}
	targetFreeMB := mustAtoi("targetFreeMB", os.Args[1])
	abortFloorMB := mustAtoi("abortFloorMB", os.Args[2])
	chunkMB := mustAtoi("chunkMB", os.Args[3])
	ttl := mustParseDuration("ttl", os.Args[4])
	poll := mustParseDuration("pollInterval", os.Args[5])

	// Clamp rather than trust. Each of these, misread, ends in an OOM-killed
	// machine, so none is allowed out of range even if the caller insists.
	targetFreeMB = max(targetFreeMB, AbsMinFreeMB)
	abortFloorMB = clamp(abortFloorMB, AbsMinFreeMB/2, targetFreeMB)
	chunkMB = clamp(chunkMB, MinChunkMB, MaxChunkMB)
	ttl = min(ttl, MaxTTL)
	poll = max(poll, MinPoll)

	sc, err := sigmaclnt.NewSigmaClnt(proc.GetProcEnv())
	if err != nil {
		db.DFatalf("Error NewSigmaClnt: %v", err)
	}

	f := &filler{chunkMB: chunkMB}
	reason := "evicted"

	// Logging the host's own view also confirms this proc reads the host's
	// /proc/meminfo rather than a namespaced one.
	total, avail, swap := mem.GetTotalMem(), mem.GetAvailableMem(), mem.GetSwapTotal()
	db.DPrintf(db.ALWAYS, "memfiller: start total=%vMB avail=%vMB swap=%vMB targetFree=%vMB abortFloor=%vMB chunk=%vMB ttl=%v poll=%v",
		total, avail, swap, targetFreeMB, abortFloorMB, chunkMB, ttl, poll)

	switch {
	case swap > 0:
		// Refuse rather than produce a number nobody can trust. With swap on,
		// pinned pages get written out under pressure, so MemAvailable stops
		// being the amount of memory the application can have without
		// thrashing -- the only reading that makes a level mean anything. The
		// harness preflights for this too, so reaching here means the check
		// was bypassed.
		db.DPrintf(db.ALWAYS, "memfiller: REFUSING to fill, swap is on (%vMB); the squeeze would be neither reproducible nor honest", swap)
	case int(avail) <= targetFreeMB:
		// Filling from here would take the host somewhere the level's label
		// does not describe.
		db.DPrintf(db.ALWAYS, "memfiller: REFUSING to fill, avail=%vMB already at or below targetFree=%vMB", avail, targetFreeMB)
	default:
		setOOMVictim()
		f.ramp(targetFreeMB, abortFloorMB)
	}

	// Started only once the ramp is done, so a caller's WaitStart means the
	// squeeze is in place rather than merely requested.
	if err := sc.Started(); err != nil {
		db.DFatalf("Error Started: %v", err)
	}

	evicted := make(chan struct{})
	go func() {
		if err := sc.WaitEvict(sc.ProcEnv().GetPID()); err != nil {
			db.DPrintf(db.ALWAYS, "memfiller: WaitEvict err %v", err)
		}
		close(evicted)
	}()

	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	deadline := time.NewTimer(ttl)
	defer deadline.Stop()

	peakMB := f.heldMB()
loop:
	for {
		select {
		case <-evicted:
			break loop
		case <-deadline.C:
			// Crash safety. Eviction is cooperative and a test that died
			// cannot send one, so without a deadline a killed test would pin
			// this memory until the machine was rebooted.
			reason = "ttl"
			db.DPrintf(db.ALWAYS, "memfiller: TTL %v expired, releasing", ttl)
			break loop
		case <-ticker.C:
			if a := int(mem.GetAvailableMem()); a < abortFloorMB {
				freed := f.shedTo(abortFloorMB)
				reason = "shed"
				db.DPrintf(db.ALWAYS, "memfiller: avail=%vMB below abortFloor=%vMB, shed %vMB (holding %vMB)",
					a, abortFloorMB, freed, f.heldMB())
			}
		}
	}

	held := f.heldMB()
	f.releaseAll()
	db.DPrintf(db.ALWAYS, "memfiller: exit reason=%v targetFree=%vMB peakHeld=%vMB finalHeld=%vMB avail=%vMB",
		reason, targetFreeMB, peakMB, held, mem.GetAvailableMem())

	if reason == "evicted" {
		sc.ClntExit(proc.NewStatus(proc.StatusEvicted))
		return
	}
	sc.ClntExitOK()
}

// ramp allocates in chunks until MemAvailable reaches targetFreeMB, re-reading
// it between every chunk.
//
// The size is measured, never computed: arithmetic on MemTotal would ignore
// whatever else is already resident, so the level reached would not be the
// level asked for. Allocate a little, look again.
func (f *filler) ramp(targetFreeMB, abortFloorMB int) {
	noProgress := 0
	for {
		avail := int(mem.GetAvailableMem())
		if avail <= targetFreeMB {
			db.DPrintf(db.ALWAYS, "memfiller: ramp done, avail=%vMB held=%vMB", avail, f.heldMB())
			return
		}
		n := min(f.chunkMB, avail-targetFreeMB)
		if avail-n < abortFloorMB {
			db.DPrintf(db.ALWAYS, "memfiller: ramp stopping short, next chunk would cross abortFloor=%vMB (avail=%vMB)", abortFloorMB, avail)
			return
		}
		if err := f.grow(n); err != nil {
			// mmap reports exhaustion as ENOMEM before any page is touched,
			// so the ramp stops with everything intact.
			db.DPrintf(db.ALWAYS, "memfiller: ramp stopping, mmap %vMB err %v (held %vMB)", n, err, f.heldMB())
			return
		}
		// Let the kernel's MemAvailable estimate catch up with the faulting
		// just done before deciding how much more to take.
		time.Sleep(20 * time.Millisecond)

		if after := int(mem.GetAvailableMem()); after >= avail {
			noProgress++
			if noProgress >= noProgressLimit {
				db.DPrintf(db.ALWAYS, "memfiller: ramp stopping, %v chunks moved avail from %vMB to %vMB; giving up rather than filling blind",
					noProgress, avail, after)
				return
			}
		} else {
			noProgress = 0
		}
	}
}

// filler owns the mapped chunks.
//
// They are mmap'd anonymous memory rather than Go slices so that releasing one
// is a munmap, which returns the memory at the syscall. A dropped []byte comes
// back only once the GC notices and the scavenger returns it, and a watchdog
// that must wait for the garbage collector to agree with it cannot bound
// anything.
type filler struct {
	mu      sync.Mutex
	chunks  [][]byte
	chunkMB int
}

func (f *filler) grow(mb int) error {
	b, err := unix.Mmap(-1, 0, mb*MB, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_ANON|unix.MAP_PRIVATE)
	if err != nil {
		return err
	}
	// Single-threaded on purpose: the ramp must not look like CPU load to
	// anything sampling the machine.
	for i := 0; i < len(b); i += pageStride {
		b[i] = 1
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chunks = append(f.chunks, b)
	return nil
}

// shedTo releases chunks until MemAvailable is back above want, and returns
// how many MB it gave up.
func (f *filler) shedTo(wantMB int) int {
	freed := 0
	for int(mem.GetAvailableMem()) < wantMB {
		n := f.shrink()
		if n == 0 {
			break
		}
		freed += n
	}
	return freed
}

func (f *filler) shrink() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.chunks) == 0 {
		return 0
	}
	b := f.chunks[len(f.chunks)-1]
	f.chunks = f.chunks[:len(f.chunks)-1]
	mb := len(b) / MB
	if err := unix.Munmap(b); err != nil {
		db.DPrintf(db.ALWAYS, "memfiller: munmap err %v", err)
		return 0
	}
	return mb
}

func (f *filler) releaseAll() {
	for f.shrink() > 0 {
	}
}

func (f *filler) heldMB() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	held := 0
	for _, b := range f.chunks {
		held += len(b) / MB
	}
	return held
}

// setOOMVictim asks the kernel to kill this proc first.
//
// If every other layer fails and the OOM killer runs, what it kills should be
// a filler holding memory nobody needs rather than etcd or named, whose death
// takes the whole SigmaOS instance with it. Raising one's own score needs no
// capability, but the write can still be refused by policy, so failure is
// logged and ignored -- this is a backstop, not a precondition.
//
// os.Getpid() rather than /proc/self: procs run in their own PID namespace, so
// this resolves to /proc/1/oom_score_adj, outside the paths the sigmaos-uproc
// AppArmor profile denies.
func setOOMVictim() {
	pn := "/proc/" + strconv.Itoa(os.Getpid()) + "/oom_score_adj"
	if err := os.WriteFile(pn, []byte("1000\n"), 0644); err != nil {
		db.DPrintf(db.ALWAYS, "memfiller: could not become the preferred OOM victim (%v): %v", pn, err)
		return
	}
	db.DPrintf(db.MEMFILLER, "memfiller: oom_score_adj=1000 via %v", pn)
}

func mustAtoi(name, s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		db.DFatalf("Error parsing %v %q: %v", name, s, err)
	}
	return n
}

func mustParseDuration(name, s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		db.DFatalf("Error parsing %v %q: %v", name, s, err)
	}
	return d
}

func clamp(v, lo, hi int) int { return min(max(v, lo), hi) }
