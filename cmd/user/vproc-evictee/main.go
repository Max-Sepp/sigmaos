// Command vproc-evictee is a proc that does nothing but be evictable. It
// exists so that a test can tell the difference between an eviction that took
// effect and one that did not.
//
// Usage: vproc-evictee <auto|bypass> <runMillis>
//
//	auto    arm vproc.AutoExitOnEvict, so an eviction ends the proc
//	bypass  ignore eviction entirely, the way a proc that never waits does
//
// Either way the proc exits StatusOK once runMillis have passed, so the
// bypass case finishes on its own rather than hanging a test.
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
		db.DFatalf("Usage: %v <auto|bypass> <runMillis>", os.Args[0])
	}
	mode := os.Args[1]
	ms, err := strconv.Atoi(os.Args[2])
	if err != nil {
		db.DFatalf("runMillis %v: %v", os.Args[2], err)
	}

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
	default:
		db.DFatalf("unknown mode %v", mode)
	}

	if err := sc.Started(); err != nil {
		db.DFatalf("Started: %v", err)
	}

	time.Sleep(time.Duration(ms) * time.Millisecond)
	sc.ClntExitOK()
}
