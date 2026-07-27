package codedmatmul

import (
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/mitchellh/mapstructure"
	"gonum.org/v1/gonum/mat"

	"sigmaos/apps/codedmatmul/mdscode"
	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/sigmaclnt"
)

// WorkerResult is what a codedmatmul-worker proc reports back via proc.Status:
// either its finished r x W block (Complete, StatusOK) or how far it got before
// being evicted (!Complete, StatusEvicted).
type WorkerResult struct {
	Idx       int
	Rows      int
	Cols      int
	Data      []float64 // row-major r x Cols block; only set if Complete
	Complete  bool
	Elapsed   time.Duration // wall-clock time spent encoding + multiplying
	SlabsDone int           // total TiledMultiply slabs actually computed, across repeats
}

// NewWorkerResult decodes a WorkerResult back out of a proc.Status's
// StatusData.
func NewWorkerResult(data interface{}) (*WorkerResult, error) {
	wr := &WorkerResult{}
	err := mapstructure.Decode(data, wr)
	return wr, err
}

func parseWorkerArgs(args []string) (idx, n, k, r, d, w, tiles, repeats int, seed int64, progressDir string, err error) {
	fields := []*int{&idx, &n, &k, &r, &d, &w, &tiles, &repeats}
	for i, f := range fields {
		*f, err = strconv.Atoi(args[i])
		if err != nil {
			return 0, 0, 0, 0, 0, 0, 0, 0, 0, "", fmt.Errorf("arg %d (%v) not an int: %w", i, args[i], err)
		}
	}
	seed, err = strconv.ParseInt(args[8], 10, 64)
	if err != nil {
		return 0, 0, 0, 0, 0, 0, 0, 0, 0, "", fmt.Errorf("seed %v not an int: %w", args[8], err)
	}
	progressDir = args[9]
	return idx, n, k, r, d, w, tiles, repeats, seed, progressDir, nil
}

// newStartedSigmaClnt connects to SigmaOS and signals that this proc has
// started running, mirroring apps/hpsearch's shared trainer boilerplate.
func newStartedSigmaClnt() (*sigmaclnt.SigmaClnt, error) {
	sc, err := sigmaclnt.NewSigmaClnt(proc.GetProcEnv())
	if err != nil {
		return nil, fmt.Errorf("NewSigmaClnt err %w", err)
	}
	if err := sc.Started(); err != nil {
		return nil, fmt.Errorf("Started err %w", err)
	}
	return sc, nil
}

// RunWorker is the entry point for the codedmatmul-worker proc. Args are
// [workerIdx, N, K, r, D, W, tiles, repeats, seed, progressDir]. It regenerates
// its A/B blocks from seed, encodes its worker index's block, tiled-multiplies
// it (repeated `repeats` times, straggler workers get repeats > 1), and reports
// the result or how far it got before eviction.
func RunWorker(args []string) {
	if len(args) != 10 {
		db.DFatalf("RunWorker: wrong number of args %v", args)
	}
	idx, n, k, r, d, w, tiles, repeats, seed, progressDir, err := parseWorkerArgs(args)
	if err != nil {
		db.DFatalf("RunWorker: %v", err)
	}
	db.DPrintf(db.CODEDMATMUL, "codedmatmul-worker start idx %d N %d K %d r %d D %d W %d tiles %d repeats %d seed %d", idx, n, k, r, d, w, tiles, repeats, seed)

	sc, err := newStartedSigmaClnt()
	if err != nil {
		db.DFatalf("RunWorker: %v", err)
	}

	blocks, B := GenBlocks(seed, k, r, d, w)
	g := mdscode.NewGenerator(n, k)
	Ahat := g.EncodeBlock(idx, blocks)

	var cancelled atomic.Bool
	go func() {
		if err := sc.WaitEvict(sc.ProcEnv().GetPID()); err != nil {
			db.DPrintf(db.CODEDMATMUL, "RunWorker %d: WaitEvict err %v", idx, err)
			return
		}
		cancelled.Store(true)
	}()

	publish := func(frac float64) {
		publishProgress(sc.FsLib, progressDir, idx, frac)
	}

	start := time.Now()
	var Y *mat.Dense
	slabsDone, complete := 0, true
	for rep := 0; rep < repeats; rep++ {
		var slabs int
		Y, slabs = mdscode.TiledMultiply(Ahat, B, r, d, w, tiles, &cancelled, publish)
		slabsDone += slabs
		if slabs < tiles {
			complete = false
			break
		}
	}
	elapsed := time.Since(start)
	db.DPrintf(db.CODEDMATMUL, "codedmatmul-worker %d done complete %v slabsDone %d elapsed %v", idx, complete, slabsDone, elapsed)

	wr := WorkerResult{Idx: idx, Rows: r, Cols: w, Complete: complete, Elapsed: elapsed, SlabsDone: slabsDone}
	if complete {
		wr.Data = Y.RawMatrix().Data
		sc.ClntExit(proc.NewStatusInfo(proc.StatusOK, "OK", wr))
	} else {
		sc.ClntExit(proc.NewStatusInfo(proc.StatusEvicted, "evicted", wr))
	}
}
