package codedmatmul

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"gonum.org/v1/gonum/mat"

	"sigmaos/apps/codedmatmul/mdscode"
	"sigmaos/proc"
	sp "sigmaos/sigmap"
	"sigmaos/valueprocs/clnt"
	"sigmaos/valueprocs/proto"
)

// WorkerVPBin is the value-procs-scheduled counterpart of WorkerBin.
const WorkerVPBin = "codedmatmul-worker-vp"

// SpawnValueProcsWorkerLeaf builds (but does not spawn -- valuesched's
// adapter does that) one worker's WorkNode.
//
// Like SpawnWorker, this proc reserves nothing. For a leaf that is not
// optional: valuesched's adapter rejects any leaf that reserves mcpu, since a
// leaf may be stopped and re-run under its own admission model rather than
// besched's mcpu-based one.
func SpawnValueProcsWorkerLeaf(idx, n, k, r, d, w, tiles, repeats int, seed int64) *clnt.WorkNode {
	args := []string{
		strconv.Itoa(idx), strconv.Itoa(n), strconv.Itoa(k), strconv.Itoa(r),
		strconv.Itoa(d), strconv.Itoa(w), strconv.Itoa(tiles), strconv.Itoa(repeats),
		strconv.FormatInt(seed, 10),
	}
	p := proc.NewProc(WorkerVPBin, args)
	return clnt.Leaf(p).WithLabel(fmt.Sprintf("w%d", idx))
}

// ValueProcsJob is StartJob/Job's counterpart, driving the same coded-matmul
// workload through valuesched instead of spawning and reaping workers by
// hand.
type ValueProcsJob struct {
	c   clnt.Runner
	cfg *Config
	tid string
	g   *mdscode.Generator
}

// StartValueProcsJob submits a Select(K, w0..w{N-1}) tree for one
// coded-matmul run: satisfied once K of N workers complete, with the
// remaining N-K as slack valuesched sheds once the quorum is reached.
func StartValueProcsJob(c clnt.Runner, cfg *Config) (*ValueProcsJob, error) {
	if cfg.M%cfg.K != 0 {
		return nil, fmt.Errorf("codedmatmul: M (%d) not divisible by K (%d)", cfg.M, cfg.K)
	}
	r := cfg.M / cfg.K

	stragglers := make(map[int]bool, len(cfg.StragglerIdx))
	for _, i := range cfg.StragglerIdx {
		stragglers[i] = true
	}

	leaves := make([]*clnt.WorkNode, cfg.N)
	for i := 0; i < cfg.N; i++ {
		repeats := 1
		if stragglers[i] {
			repeats = cfg.Repeats
		}
		leaves[i] = SpawnValueProcsWorkerLeaf(i, cfg.N, cfg.K, r, cfg.D, cfg.W, cfg.Tiles, repeats, cfg.Seed)
	}
	root, err := clnt.Select(cfg.K, leaves...)
	if err != nil {
		return nil, err
	}

	tid := "codedmatmul-" + sp.GenPid("").String()
	if _, err := c.Submit(tid, "codedmatmul", root); err != nil {
		return nil, err
	}
	return &ValueProcsJob{c: c, cfg: cfg, tid: tid, g: mdscode.NewGenerator(cfg.N, cfg.K)}, nil
}

// Wait blocks until the tree's K-of-N quorum settles, decodes C from
// whichever K workers' results arrived, and returns a Result/Sample shape
// Analyze can consume exactly as it does for the baseline Job.Wait.
//
// QuorumStats.NEvicted is always 0 for this arm: valuesched does not report
// which surplus attempts it stopped as a count a submitter can read as an
// eviction tally (see NAttemptsStopped below for the closest analog).
func (j *ValueProcsJob) Wait() (*mat.Dense, []*Sample, QuorumStats, error) {
	start := time.Now()
	results, _, err := j.c.Wait(j.tid)
	if err != nil {
		return nil, nil, QuorumStats{}, err
	}
	stats := QuorumStats{Makespan: time.Since(start)}

	finishedBlocks := map[int]*mat.Dense{}
	samples := make([]*Sample, 0, len(results))
	for _, res := range results {
		wr, err := NewWorkerResult(res.Status.Data())
		if err != nil {
			return nil, nil, stats, fmt.Errorf("codedmatmul valueprocs: decode %v: %w", res.Label, err)
		}
		finishedBlocks[wr.Idx] = mat.NewDense(wr.Rows, wr.Cols, wr.Data)
		samples = append(samples, &Sample{Idx: wr.Idx, WR: wr, Used: true})
		// The tree stops the surplus once K leaves complete, so whoever
		// completed is whoever the job did not wait past.
		stats.QuorumIdx = append(stats.QuorumIdx, wr.Idx)
	}
	sort.Slice(samples, func(a, b int) bool { return samples[a].Idx < samples[b].Idx })

	C, err := mdscode.Decode(j.g, finishedBlocks)
	if err != nil {
		return nil, nil, stats, err
	}
	return C, samples, stats, nil
}

// NAttemptsStopped reports how many attempts across the tree's leaves the
// scheduler stopped (summing each leaf's cumulative Stops), sampled right
// after Wait returns. It is an attempt count, not core-seconds, and is the
// closest available analog to Result.NEvicted -- the client API exposes no
// per-attempt elapsed time for an attempt that never reached Complete, so a
// faithful WastedCoreSeconds cannot be reconstructed for this arm.
//
// NCharged (TreeStatusRep's other count) is unsuitable for this: it is a
// live occupancy gauge, not a cumulative counter, so by the time Wait has
// returned and the tree has fully settled, it reads near zero regardless of
// how much surplus the scheduler actually shed along the way.
// Status returns the tree's raw status, for callers that want the full
// per-leaf breakdown (state, run count, stops, score) rather than just the
// aggregate NAttemptsStopped.
func (j *ValueProcsJob) Status() (*proto.TreeStatusRep, error) {
	return j.c.Status(j.tid)
}

func (j *ValueProcsJob) NAttemptsStopped() (int, error) {
	st, err := j.c.Status(j.tid)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, node := range st.Nodes {
		if node.IsLeaf {
			n += int(node.Stops)
		}
	}
	return n, nil
}
