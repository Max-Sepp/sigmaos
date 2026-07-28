package codedmatmul

import (
	"fmt"
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
type Config struct {
	M, D, W      int
	N, K         int
	Repeats      int   // recompute factor given to straggler workers
	StragglerIdx []int // worker indices that get Repeats instead of 1 (i.e. list of workers which are made stragglers)
	Tiles        int   // TiledMultiply slab count
	Mcpu         proc.Tmcpu
	Mem          proc.Tmem // declared memory reservation per worker; 0 (default) leaves workers unconstrained by besched's memory-based admission
	Seed         int64
}

func DefaultConfig() *Config {
	return &Config{
		M: 384, D: 65536, W: 64,
		N: 9, K: 6,
		Repeats:      4,
		StragglerIdx: []int{0},
		Tiles:        8,
		Mcpu:         1000,
		Seed:         7159623,
	}
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
func SpawnWorker(sc *sigmaclnt.SigmaClnt, idx, n, k, r, d, w, tiles, repeats int, seed int64, progressDir string, mcpu proc.Tmcpu, mem proc.Tmem) (*proc.Proc, error) {
	args := []string{
		strconv.Itoa(idx), strconv.Itoa(n), strconv.Itoa(k), strconv.Itoa(r),
		strconv.Itoa(d), strconv.Itoa(w), strconv.Itoa(tiles), strconv.Itoa(repeats),
		strconv.FormatInt(seed, 10), progressDir,
	}
	p := proc.NewProc(WorkerBin, args)
	p.SetMcpu(mcpu)
	if mem > 0 {
		p.SetMem(mem)
	}
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

	stragglers := make(map[int]bool, len(cfg.StragglerIdx))
	for _, i := range cfg.StragglerIdx {
		stragglers[i] = true
	}

	procs := make([]*proc.Proc, cfg.N)
	for i := 0; i < cfg.N; i++ {
		repeats := 1
		if stragglers[i] {
			repeats = cfg.Repeats
		}
		p, err := SpawnWorker(sc, i, cfg.N, cfg.K, r, cfg.D, cfg.W, cfg.Tiles, repeats, cfg.Seed, progressDir, cfg.Mcpu, cfg.Mem)
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
			if cancelSurplus && !reaped {
				reaped = true
				// Evict is synchronous but only blocks this
				// loop, not the WaitExit goroutines above.
				for i, p := range j.procs {
					if _, ok := finishedBlocks[i]; !ok {
						if err := j.sc.Evict(p.GetPid()); err == nil {
							stats.NEvicted++
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
