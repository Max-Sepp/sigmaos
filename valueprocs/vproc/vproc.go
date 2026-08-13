// Package vproc is the proc-side runtime for work run under the value-proc
// scheduler.
//
// It is not a framework: it does not own main, does not drive the work loop,
// and defines no interface to implement. A proc calls it, never the reverse.
package vproc

import (
	"os"

	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/sigmaclnt"
)

// AutoExitOnEvict makes this proc die promptly when its parent evicts it.
//
// Eviction in SigmaOS is advisory: it sets a flag and wakes anyone waiting on
// it, and nothing else in the system touches the process. A proc that never
// waits therefore runs to completion holding the resources its parent has
// already decided to reclaim, and the eviction is silently a no-op. This is
// the whole of the obligation, in one call.
//
// Call it once, early. It is safe on any proc, not just those scheduled by
// this layer, and it is the only thing in this package with no dependency on
// the rest of it.
//
// Do not use it on a proc that has partial progress worth reporting: it
// terminates on the spot and reports nothing beyond the eviction itself. Such
// a proc should wait on eviction itself and exit with what it has.
func AutoExitOnEvict(sc *sigmaclnt.SigmaClnt) {
	pid := sc.ProcEnv().GetPID()
	go func() {
		if err := sc.WaitEvict(pid); err != nil {
			// Nothing to do but stop watching. The proc keeps running, and
			// the parent's own timeout is what covers this.
			db.DPrintf(db.VALUEPROC_ERR, "AutoExitOnEvict %v: WaitEvict err %v", pid, err)
			return
		}
		db.DPrintf(db.VALUEPROC, "AutoExitOnEvict %v: evicted", pid)

		// Report before terminating. ClntExit does not end the process -- it
		// reports the status, ends leases and closes the FsLib -- and doing
		// it first is what lets msched run its exit path, release whoever is
		// waiting and free the accounting. Exiting first would leave msched
		// to notice only when the container died and to synthesize a crash
		// status on this proc's behalf, so a routine reclamation would read
		// as a failure.
		if err := sc.ClntExit(proc.NewStatus(proc.StatusEvicted)); err != nil {
			db.DPrintf(db.VALUEPROC_ERR, "AutoExitOnEvict %v: ClntExit err %v", pid, err)
		}

		// A goroutine cannot return from main, so the process ends here.
		// Zero, because the real status has already travelled via Exited and
		// a nonzero code would read as a crash in msched's logs.
		os.Exit(0)
	}()
}
