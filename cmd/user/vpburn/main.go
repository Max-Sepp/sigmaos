// Command vpburn is a value proc that occupies a core and says so. It exists
// so a benchmark can put a second tree on a cluster and watch what the first
// one gives up.
//
// Usage: vpburn <burnMillis> [reportMillis]
//
// burnMillis is nominal: the work one unoccupied core would do in that time,
// so a burner sharing a core takes proportionally longer. Eviction is vproc's
// default, which ends the proc at once -- there is no partial progress here
// worth handing back.
package main

import (
	"os"
	"strconv"
	"time"

	db "sigmaos/debug"
	"sigmaos/util/burn"
	"sigmaos/valueprocs/vproc"
)

// ReportEvery is how often progress is pushed.
//
// A competitor has to report something. An attempt that never has is held
// against the probe budget rather than a slot, so a silent burner would take
// no capacity from anyone and squeeze nothing.
const ReportEvery = 250 * time.Millisecond

// Slice is how much work is burned between reports, so an eviction lands
// promptly rather than at the end of the run.
const Slice = 20 * time.Millisecond

func main() {
	if len(os.Args) < 2 || len(os.Args) > 3 {
		db.DFatalf("Usage: %v <burnMillis> [reportMillis]", os.Args[0])
	}
	burnMs, err := strconv.Atoi(os.Args[1])
	if err != nil || burnMs <= 0 {
		db.DFatalf("burnMillis %v: want a positive int (%v)", os.Args[1], err)
	}
	report := ReportEvery
	if len(os.Args) == 3 {
		ms, err := strconv.Atoi(os.Args[2])
		if err != nil || ms <= 0 {
			db.DFatalf("reportMillis %v: want a positive int (%v)", os.Args[2], err)
		}
		report = time.Duration(ms) * time.Millisecond
	}

	c, err := vproc.Start(vproc.WithScoreInterval(report))
	if err != nil {
		db.DFatalf("vproc.Start: %v", err)
	}

	total := time.Duration(burnMs) * time.Millisecond
	for done := time.Duration(0); done < total; done += Slice {
		burn.For(min(Slice, total-done))
		// Gradient one is "covering its work in the time it expected to". A
		// competitor that looked stalled would invite the scheduler to race
		// it, which is a different experiment from taking capacity off it.
		c.Score(float64(done+Slice)/float64(total), 1)
	}
	db.DPrintf(db.ALWAYS, "vpburn: burned %v", total)
	c.Complete(nil)
}
