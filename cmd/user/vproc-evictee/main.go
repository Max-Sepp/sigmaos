// Command vproc-evictee is a proc that does nothing but be evictable. It
// exists so that a test can tell the difference between an eviction that took
// effect and one that did not.
//
// Usage: vproc-evictee <auto|bypass|graceful> <runMillis>
//
//	auto      arm vproc.AutoExitOnEvict, so an eviction ends the proc
//	bypass    ignore eviction entirely, the way a proc that never waits does
//	graceful  watch for the eviction and report what it got through
//
// Every mode exits StatusOK once runMillis have passed, so the bypass case
// finishes on its own rather than hanging a test.
package main

import (
	"os"
	"strconv"
	"time"

	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/sigmaclnt"
	"sigmaos/valueprocs/vproc"
)

func main() {
	if len(os.Args) != 3 {
		db.DFatalf("Usage: %v <auto|bypass|graceful> <runMillis>", os.Args[0])
	}
	mode := os.Args[1]
	ms, err := strconv.Atoi(os.Args[2])
	if err != nil {
		db.DFatalf("runMillis %v: %v", os.Args[2], err)
	}
	dur := time.Duration(ms) * time.Millisecond

	sc, err := sigmaclnt.NewSigmaClnt(proc.GetProcEnv())
	if err != nil {
		db.DFatalf("NewSigmaClnt: %v", err)
	}

	switch mode {
	case "auto":
		vproc.AutoExitOnEvict(sc)
	case "bypass":
		// Deliberately nothing. This is what every proc that does not wait on
		// its eviction looks like from the outside.
	case "graceful":
		// Reports Started itself, so it takes the whole rest of the proc.
		runGracefully(sc, dur)
		return
	default:
		db.DFatalf("unknown mode %v", mode)
	}

	if err := sc.Started(); err != nil {
		db.DFatalf("Started: %v", err)
	}

	time.Sleep(dur)
	sc.ClntExitOK()
}

// tick is how often the graceful case counts another unit of work, so that a
// test can tell a proc that got somewhere before it was asked to stop from
// one that reported an empty hand.
const tick = 50 * time.Millisecond

// runGracefully stops on its own terms: it watches for the eviction rather
// than dying on it, and hands back what it got through.
//
// This is the one case the default is wrong for. Immediate exit is safe only
// because leaves are required to be idempotent, and a proc whose partial
// progress is genuinely worth consuming would be throwing it away.
func runGracefully(sc *sigmaclnt.SigmaClnt, dur time.Duration) {
	c, err := vproc.StartWith(sc, vproc.WithGracefulEvict())
	if err != nil {
		db.DFatalf("vproc.StartWith: %v", err)
	}
	deadline := time.After(dur)
	iters := 0
	for {
		select {
		case <-c.Done():
			db.DPrintf(db.VALUEPROC, "graceful: asked to stop after %v iters", iters)
			c.StoppedWith(map[string]any{"iters": iters})
			return
		case <-deadline:
			c.Complete(map[string]any{"iters": iters})
			return
		case <-time.After(tick):
			iters++
		}
	}
}
