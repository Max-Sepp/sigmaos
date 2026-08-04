package codedmatmul

import (
	"fmt"
	"strconv"
	"time"

	"gonum.org/v1/gonum/mat"

	"sigmaos/apps/codedmatmul/mdscode"
	db "sigmaos/debug"
	"sigmaos/valueprocs/vproc"
)

func parseValueProcsWorkerArgs(args []string) (idx, n, k, r, d, w, tiles, repeats int, seed int64, err error) {
	fields := []*int{&idx, &n, &k, &r, &d, &w, &tiles, &repeats}
	for i, f := range fields {
		*f, err = strconv.Atoi(args[i])
		if err != nil {
			return 0, 0, 0, 0, 0, 0, 0, 0, 0, fmt.Errorf("arg %d (%v) not an int: %w", i, args[i], err)
		}
	}
	seed, err = strconv.ParseInt(args[8], 10, 64)
	if err != nil {
		return 0, 0, 0, 0, 0, 0, 0, 0, 0, fmt.Errorf("seed %v not an int: %w", args[8], err)
	}
	return idx, n, k, r, d, w, tiles, repeats, seed, nil
}

// RunValueProcsWorker is the entry point for the codedmatmul-worker-vp proc,
// the value-procs-scheduled counterpart of RunWorker.
//
// Args are [workerIdx, N, K, r, D, W, tiles, repeats, seed] -- the same as
// RunWorker, minus progressDir: there is no separate progress-broadcast
// mechanism here, since c.Score reports progress to the scheduler directly.
//
// Cancellation and scoring both replace hand-rolled machinery RunWorker
// still carries: vproc.Start's default self-termination on eviction stands
// in for the manual WaitEvict goroutine, and c.CancelledFlag() is handed
// straight to TiledMultiply in place of that goroutine's atomic.Bool, since
// TiledMultiply already takes exactly that type.
func RunValueProcsWorker(args []string) {
	if len(args) != 9 {
		db.DFatalf("RunValueProcsWorker: wrong number of args %v", args)
	}
	idx, n, k, r, d, w, tiles, repeats, seed, err := parseValueProcsWorkerArgs(args)
	if err != nil {
		db.DFatalf("RunValueProcsWorker: %v", err)
	}
	db.DPrintf(db.CODEDMATMUL, "codedmatmul-worker-vp start idx %d N %d K %d r %d D %d W %d tiles %d repeats %d seed %d", idx, n, k, r, d, w, tiles, repeats, seed)

	c, err := vproc.Start()
	if err != nil {
		db.DFatalf("RunValueProcsWorker: vproc.Start: %v", err)
	}

	blocks, B := GenBlocks(seed, k, r, d, w)
	g := mdscode.NewGenerator(n, k)
	Ahat := g.EncodeBlock(idx, blocks)

	// Gradient is always 0: racing a stalled slab of the same worker has no
	// meaning here, since redundancy across workers is already the tree's
	// k-of-n slack.
	publish := func(frac float64) { c.Score(frac, 0) }

	start := time.Now()
	var Y *mat.Dense
	slabsDone, complete := 0, true
	for rep := 0; rep < repeats; rep++ {
		var slabs int
		Y, slabs = mdscode.TiledMultiply(Ahat, B, r, d, w, tiles, c.CancelledFlag(), publish)
		slabsDone += slabs
		if slabs < tiles {
			complete = false
			break
		}
	}
	elapsed := time.Since(start)
	db.DPrintf(db.CODEDMATMUL, "codedmatmul-worker-vp %d done complete %v slabsDone %d elapsed %v", idx, complete, slabsDone, elapsed)

	wr := WorkerResult{Idx: idx, Rows: r, Cols: w, Complete: complete, Elapsed: elapsed, SlabsDone: slabsDone}
	if complete {
		wr.Data = Y.RawMatrix().Data
		c.Complete(wr)
	} else {
		// Only reachable if TiledMultiply returns early for a reason other
		// than eviction (e.g. a future error path); default self-termination
		// means eviction itself never gets here.
		c.Fatal(fmt.Errorf("codedmatmul-worker-vp %d: incomplete after %d/%d slabs", idx, slabsDone, tiles*repeats))
	}
}
