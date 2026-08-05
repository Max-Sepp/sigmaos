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

	// What this worker reports about itself: progress through the whole job,
	// and the slope of that progress.
	//
	// The score has to span the whole job rather than the current pass.
	// TiledMultiply publishes t+1 over T and is called once per repeat, so the
	// fraction it hands out runs 1/T to 1 and then starts over. Forwarding that
	// directly would make a worker's score collapse to near zero at every
	// repeat boundary, and since Repeats above 1 is exactly what makes a worker
	// a straggler, the workers whose reports matter most would be the ones
	// misreported. A score is how much value has been realized so far, and
	// realized value does not go backwards.
	//
	// The gradient is that progress differentiated against time. The scheduler
	// extrapolates along it, so reporting a flat curve while the score climbs
	// would be a claim never to finish.
	//
	// Normalizing against one pass, rather than against this worker's own
	// total, is what makes a straggler visible. A worker is handed no expected
	// duration, so its first slab sets the scale: one pass is tiles slabs, and
	// a worker doing four passes covers the job at a quarter of the rate. Were
	// each worker normalized against its own workload they would all report 1
	// and a straggler would be indistinguishable from a healthy peer, which is
	// the one comparison this application exists to make.
	var (
		reps     int     // repeats completed, counted by the fraction wrapping
		lastFrac float64 // last within-pass fraction, to notice the wrap
		lastDone float64
		lastAt   = time.Now()
		perSlab  time.Duration
	)
	publish := func(frac float64) {
		now := time.Now()
		if frac < lastFrac {
			reps++
		}
		lastFrac = frac
		done := (float64(reps) + frac) / float64(repeats)

		dt := now.Sub(lastAt)
		if perSlab <= 0 {
			// The first slab is what sets the scale, so there is nothing to
			// measure it against yet and nothing is claimed.
			perSlab, lastDone, lastAt = dt, done, now
			c.Score(done, 0)
			return
		}
		grad := 0.0
		if dt > 0 {
			onePass := time.Duration(tiles) * perSlab
			grad = (done - lastDone) / (dt.Seconds() / onePass.Seconds())
		}
		lastDone, lastAt = done, now
		c.Score(done, grad)
	}

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
