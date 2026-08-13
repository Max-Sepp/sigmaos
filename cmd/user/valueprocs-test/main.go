// Command valueprocs-test is a workload for testing the value-proc
// scheduler. It does no real work; it reports scores on a trajectory a test
// chooses and ends however the test asks it to.
//
// Usage: valueprocs-test <mode> <runMillis> [finalScore] [gradient]
//
//	work    report a score rising to finalScore, then succeed
//	stall   report a flat score with a high gradient, the shape of a
//	        straggler that would benefit from being raced
//	fail    exit with a retryable error
//	fatal   exit with an error no number of retries would fix
//	bypass  ignore eviction entirely, the way a proc that never waits does
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/sigmaclnt"
	"sigmaos/valueprocs/vproc"
)

const tick = 50 * time.Millisecond

func main() {
	if len(os.Args) < 3 {
		db.DFatalf("Usage: %v <mode> <runMillis> [finalScore] [gradient]", os.Args[0])
	}
	mode := os.Args[1]
	ms, err := strconv.Atoi(os.Args[2])
	if err != nil {
		db.DFatalf("runMillis %v: %v", os.Args[2], err)
	}
	final := argFloat(3, 1.0)
	grad := argFloat(4, 0.0)
	dur := time.Duration(ms) * time.Millisecond

	if mode == "bypass" {
		runBypassing(dur)
		return
	}

	c, err := vproc.Start()
	if err != nil {
		db.DFatalf("vproc.Start: %v", err)
	}
	db.DPrintf(db.VALUEPROC, "valueprocs-test %v: node %v, mode %v for %v",
		os.Args[0], c.NodeID(), mode, dur)

	switch mode {
	case "fail":
		c.Fail(fmt.Errorf("valueprocs-test: asked to fail"))
		return
	case "fatal":
		c.Fatal(fmt.Errorf("valueprocs-test: asked to fail fatally"))
		return
	case "work", "stall":
	default:
		c.Fatal(fmt.Errorf("valueprocs-test: unknown mode %v", mode))
		return
	}

	start := time.Now()
	iters := 0
	for {
		elapsed := time.Since(start)
		if elapsed >= dur {
			break
		}
		iters++
		if mode == "stall" {
			// Flat score, high gradient: no progress, and racing would help.
			c.Score(final, grad)
		} else {
			c.Score(final*float64(elapsed)/float64(dur), grad)
		}
		time.Sleep(tick)
	}

	// The payload travels home inside proc.Status, which is the only channel
	// there is: the scheduler spawned this proc, so whoever submitted the
	// tree is not its parent and cannot collect an exit status directly.
	c.Complete(map[string]any{
		"node":  c.NodeID(),
		"iters": iters,
		"score": final,
	})
}

func argFloat(i int, def float64) float64 {
	if len(os.Args) <= i {
		return def
	}
	f, err := strconv.ParseFloat(os.Args[i], 64)
	if err != nil {
		return def
	}
	return f
}

// runBypassing is a proc that never waits on its eviction, which is what
// every proc looked like before this layer existed. It is here so a test can
// tell an eviction that took effect from one that did not.
func runBypassing(dur time.Duration) {
	sc, err := sigmaclnt.NewSigmaClnt(proc.GetProcEnv())
	if err != nil {
		db.DFatalf("NewSigmaClnt: %v", err)
	}
	if err := sc.Started(); err != nil {
		db.DFatalf("Started: %v", err)
	}
	time.Sleep(dur)
	sc.ClntExit(proc.NewStatusInfo(proc.StatusOK, "", map[string]any{"bypassed": true}))
}
