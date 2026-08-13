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

func parseValueProcsWorkerArgs(args []string) (idx, n, k, r, d, w, tiles, repeats, passes int, seed int64, err error) {
	fields := []*int{&idx, &n, &k, &r, &d, &w, &tiles, &repeats, &passes}
	for i, f := range fields {
		*f, err = strconv.Atoi(args[i])
		if err != nil {
			return 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, fmt.Errorf("arg %d (%v) not an int: %w", i, args[i], err)
		}
	}
	seed, err = strconv.ParseInt(args[9], 10, 64)
	if err != nil {
		return 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, fmt.Errorf("seed %v not an int: %w", args[9], err)
	}
	return idx, n, k, r, d, w, tiles, repeats, passes, seed, nil
}

// RunValueProcsWorker is the entry point for the codedmatmul-worker-vp proc,
// the value-procs-scheduled counterpart of RunWorker.
//
// Args are [workerIdx, N, K, r, D, W, tiles, repeats, passes, seed]: RunWorker's
// list minus progressDir, since c.Score reports progress to the scheduler
// directly rather than broadcasting it to a directory, and plus passes, the
// nominal workload this worker's rate is reported against.
//
// Cancellation and scoring both replace hand-rolled machinery RunWorker
// still carries: vproc.Start's default self-termination on eviction stands
// in for the manual WaitEvict goroutine, and c.CancelledFlag() is handed
// straight to TiledMultiply in place of that goroutine's atomic.Bool, since
// TiledMultiply already takes exactly that type.
func RunValueProcsWorker(args []string) {
	if len(args) != 10 {
		db.DFatalf("RunValueProcsWorker: wrong number of args %v", args)
	}
	idx, n, k, r, d, w, tiles, repeats, passes, seed, err := parseValueProcsWorkerArgs(args)
	if err != nil {
		db.DFatalf("RunValueProcsWorker: %v", err)
	}
	db.DPrintf(db.CODEDMATMUL, "codedmatmul-worker-vp start idx %d N %d K %d r %d D %d W %d tiles %d repeats %d passes %d seed %d", idx, n, k, r, d, w, tiles, repeats, passes, seed)

	// Graceful, so that a worker the scheduler sheds can say how far it got.
	// Under the default the proc is killed where it stands, and the slabs it
	// computed are lost -- which for the surplus of a coded quorum is exactly
	// the work the arm is supposed to be judged on not having wasted.
	c, err := vproc.Start(vproc.WithGracefulEvict())
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
	// Normalizing against what an ordinary worker is asked for, rather than
	// against this worker's own total, is what makes a straggler visible. A
	// worker is handed no expected duration, so its first slab sets the scale:
	// a nominal workload is passes*tiles slabs, and a worker doing four times
	// that covers the job at a quarter of the rate. Were each worker
	// normalized against its own workload they would all report 1 and a
	// straggler would be indistinguishable from a healthy peer, which is the
	// one comparison this application exists to make.
	//
	// The nominal count travels with the work rather than being assumed to be
	// one pass. Assuming it would make every worker report 1/passes once the
	// job is lengthened, which is not a claim about being slow -- it is the
	// same healthy rate against a longer yardstick -- and it would push every
	// incumbent's value below what an unproven candidate is presumed to be
	// worth, inviting the scheduler to swap running work for guesses.
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
			nominal := time.Duration(passes*tiles) * perSlab
			grad = (done - lastDone) / (dt.Seconds() / nominal.Seconds())
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
	switch {
	case complete:
		wr.Data = Y.RawMatrix().Data
		c.Complete(wr)
	case c.Cancelled():
		// Shed as surplus. The block is unusable and deliberately not sent,
		// but the slab count is what the surplus cost and is the only account
		// of it: a stopped attempt reports no result, so without this the
		// work it did before being stopped is invisible to whoever submitted
		// the tree.
		c.StoppedWith(wr)
	default:
		c.Fatal(fmt.Errorf("codedmatmul-worker-vp %d: incomplete after %d/%d slabs", idx, slabsDone, tiles*repeats))
	}
}
