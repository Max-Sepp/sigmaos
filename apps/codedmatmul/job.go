package codedmatmul

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"gonum.org/v1/gonum/mat"

	"sigmaos/apps/codedmatmul/mdscode"
	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/sigmaclnt"
	sp "sigmaos/sigmap"
)

const (
	WorkerBin = "codedmatmul-worker"

	ProgressDirTop = sp.NAMED + "codedmatmul-progress/"
)

// Config is one coded-matmul run: C = A*B split into N workers, K of which are
// needed to reconstruct C. A is M x D (M = K*r row-stripes of r x D), B is D x
// W.
//
// No arm declares a resource reservation. adapter/spec.go rejects any leaf
// that reserves mcpu, so the value-procs arm could never match a reservation
// the classical arms held; equalizing downward is what makes the arms differ
// only in how they are scheduled rather than also in how they are admitted.
// Analyze charges a worker one core regardless, which is what a worker
// actually burns.
type Config struct {
	M, D, W      int
	N, K         int
	Repeats      int   // recompute factor given to straggler workers
	StragglerIdx []int // worker indices that get Repeats instead of 1 (i.e. list of workers which are made stragglers)
	Tiles        int   // TiledMultiply slab count
	Seed         int64

	// Passes is how many times an ordinary worker recomputes its block, and is
	// how this workload is made to last longer. Zero means one.
	//
	// Work scales with it and memory does not, which is why it exists: the
	// multiply is O(r*D*W) but the operands are O(r*D + D*W), so lengthening
	// the job by scaling any matrix dimension costs gigabytes across N workers
	// while repeating the multiply costs nothing. A straggler does Passes
	// times Repeats, so the ratio that makes it a straggler is unchanged.
	//
	// It matters because a scheduling decision takes time to make. A job that
	// finishes before its scheduler can propose, hold and apply a change to
	// how many workers run has not tested the scheduler; it has measured how
	// long the job takes to start.
	Passes int
}

// passes is Passes with its zero value read as one, so a Config written before
// this field existed still describes the job it used to.
func (c *Config) passes() int {
	if c.Passes <= 0 {
		return 1
	}
	return c.Passes
}

// repeatsFor is how many passes worker idx runs: the nominal count, times the
// straggler factor if this is one of the stragglers.
func (c *Config) repeatsFor(idx int) int {
	for _, s := range c.StragglerIdx {
		if s == idx {
			return c.passes() * c.Repeats
		}
	}
	return c.passes()
}

func DefaultConfig() *Config {
	return &Config{
		M: 384, D: 65536, W: 64,
		N: 9, K: 6,
		Repeats:      4,
		StragglerIdx: []int{0},
		Tiles:        8,
		Seed:         7159623,
		Passes:       1,
	}
}

// LongPasses is what the long-running arm sets Passes to.
//
// Chosen from the measured short run rather than picked: an ordinary worker
// covers its eight slabs in about two seconds there, so ten passes puts every
// arm in the twenty-second range. That is long enough for the tree to open at
// N, be told which workers are ahead, hold the proposal to narrow, and run
// narrowed for most of the job -- the sequence the short arm cannot complete
// before it finishes.
const LongPasses = 10

// LongConfig is DefaultConfig scaled up in time and nothing else.
//
// Same matrices, same quorum, same straggler, same memory: only how many times
// each worker recomputes its block changes, so a result here is comparable
// with the short arm's rather than being a different experiment.
func LongConfig() *Config {
	c := DefaultConfig()
	c.Passes = LongPasses
	return c
}

type Job struct {
	sc          *sigmaclnt.SigmaClnt
	cfg         *Config
	procs       []*proc.Proc
	g           *mdscode.Generator
	progressDir string
	waitedOn    bool
}

// Procs returns the job's worker procs, in worker-index order.
func (j *Job) Procs() []*proc.Proc {
	return j.procs
}

func (j *Job) WaitStart() error {
	for _, p := range j.procs {
		if err := j.sc.WaitStart(p.GetPid()); err != nil {
			return err
		}
	}
	return nil
}

// SpawnWorker spawns a single codedmatmul-worker proc, without waiting for it
// to start running.
func SpawnWorker(sc *sigmaclnt.SigmaClnt, idx, n, k, r, d, w, tiles, repeats int, seed int64, progressDir string) (*proc.Proc, error) {
	args := []string{
		strconv.Itoa(idx), strconv.Itoa(n), strconv.Itoa(k), strconv.Itoa(r),
		strconv.Itoa(d), strconv.Itoa(w), strconv.Itoa(tiles), strconv.Itoa(repeats),
		strconv.FormatInt(seed, 10), progressDir,
	}
	p := proc.NewProc(WorkerBin, args)
	if err := sc.Spawn(p); err != nil {
		return nil, err
	}
	return p, nil
}

// StartJob spawns cfg.N workers for one coded-matmul run.
//
// cfg.N == cfg.K gives the uncoded-barrier arm (no redundancy).
//
// cfg.N > cfg.K gives a coded arm, whose surplus is either left to run to
// completion or reaped once a quorum finishes, depending on the cancelSurplus
// flag passed to Wait.
func StartJob(sc *sigmaclnt.SigmaClnt, cfg *Config) (*Job, error) {
	if cfg.M%cfg.K != 0 {
		return nil, fmt.Errorf("codedmatmul: M (%d) not divisible by K (%d)", cfg.M, cfg.K)
	}
	r := cfg.M / cfg.K

	progressDir := ProgressDirTop + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := sc.MkDirPath(sp.NAMED, strings.TrimPrefix(progressDir, sp.NAMED), 0777); err != nil {
		return nil, err
	}

	procs := make([]*proc.Proc, cfg.N)
	for i := 0; i < cfg.N; i++ {
		p, err := SpawnWorker(sc, i, cfg.N, cfg.K, r, cfg.D, cfg.W, cfg.Tiles,
			cfg.repeatsFor(i), cfg.Seed, progressDir)
		if err != nil {
			return nil, err
		}
		procs[i] = p
	}
	return &Job{sc: sc, cfg: cfg, procs: procs, g: mdscode.NewGenerator(cfg.N, cfg.K), progressDir: progressDir}, nil
}

// Wait reaps every worker, decodes C as soon as a K-quorum of results is in
// (evicting the rest first if cancelSurplus is set), and returns C plus a
// per-worker Sample of what each contributed, for Analyze.
//
// Should only be called once per job since it cleans up the job's progress
// directory and marks the job as waited on.
func (j *Job) Wait(cancelSurplus bool) (*mat.Dense, []*Sample, QuorumStats, error) {
	if j.waitedOn {
		return nil, nil, QuorumStats{}, fmt.Errorf("Wait called more than once on this job")
	}
	j.waitedOn = true
	defer func() {
		if err := j.sc.RmDir(j.progressDir); err != nil {
			db.DPrintf(db.CODEDMATMUL, "Job.Wait: RmDir %v err %v", j.progressDir, err)
		}
	}()

	type workerDone struct {
		idx int
		wr  *WorkerResult
		ok  bool
	}

	// One goroutine per worker, each blocked on its own WaitExit. ch is
	// buffered so sends never block, even while the loop below is busy.
	ch := make(chan workerDone, len(j.procs))
	for i, p := range j.procs {
		go func(i int, p *proc.Proc) {
			wd := workerDone{idx: i}
			st, err := j.sc.WaitExit(p.GetPid())
			if err == nil && (st.IsStatusOK() || st.IsStatusEvicted()) {
				if wr, derr := NewWorkerResult(st.Data()); derr == nil {
					wd.wr = wr
					wd.ok = st.IsStatusOK()
				}
			}
			ch <- wd
		}(i, p)
	}

	start := time.Now()
	finishedBlocks := map[int]*mat.Dense{}
	results := make(map[int]*WorkerResult, len(j.procs))
	var stats QuorumStats
	quorumReached, reaped := false, false

	// Sole consumer of ch, so the state below needs no locking. Bounded by
	// received count, not quorum size, so it terminates even though evicted
	// workers never satisfy wd.ok.
	for received := 0; received < len(j.procs); received++ {
		wd := <-ch
		if wd.wr != nil {
			results[wd.idx] = wd.wr
		}
		if wd.ok {
			finishedBlocks[wd.idx] = mat.NewDense(wd.wr.Rows, wd.wr.Cols, wd.wr.Data)
		}
		if !quorumReached && len(finishedBlocks) >= j.cfg.K {
			quorumReached = true
			stats.Makespan = time.Since(start)
			done := make([]int, 0, len(finishedBlocks))
			for i := range finishedBlocks {
				done = append(done, i)
			}
			sort.Ints(done)
			stats.QuorumIdx = slices.Clone(done)
			db.DPrintf(db.ALWAYS, "Job.Wait: quorum of %d reached at %v via workers %v", j.cfg.K, stats.Makespan, done)
			if cancelSurplus && !reaped {
				reaped = true
				// Evict is synchronous but only blocks this
				// loop, not the WaitExit goroutines above.
				for i, p := range j.procs {
					if _, ok := finishedBlocks[i]; !ok {
						if err := j.sc.Evict(p.GetPid()); err == nil {
							stats.NEvicted++
							db.DPrintf(db.ALWAYS, "Job.Wait: evicting surplus worker %d (%v), not needed for quorum", i, p.GetPid())
						} else {
							db.DPrintf(db.ALWAYS, "Job.Wait: Evict worker %d (%v) err %v", i, p.GetPid(), err)
						}
					}
				}
			}
		}
	}

	C, err := mdscode.Decode(j.g, finishedBlocks)
	if err != nil {
		return nil, nil, stats, err
	}

	used := usedIndices(finishedBlocks, j.cfg.K)
	samples := make([]*Sample, 0, len(results))
	for idx, wr := range results {
		samples = append(samples, &Sample{Idx: idx, WR: wr, Used: used[idx]})
	}
	sort.Slice(samples, func(a, b int) bool { return samples[a].Idx < samples[b].Idx })

	return C, samples, stats, nil
}

// usedIndices mirrors mdscode.Decode's own selection (sorted ascending, first
// K) so callers can tell which workers' compute actually contributed to the
// decoded result.
func usedIndices(finishedBlocks map[int]*mat.Dense, k int) map[int]bool {
	idxs := make([]int, 0, len(finishedBlocks))
	for i := range finishedBlocks {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	if len(idxs) > k {
		idxs = idxs[:k]
	}
	used := make(map[int]bool, len(idxs))
	for _, i := range idxs {
		used[i] = true
	}
	return used
}
