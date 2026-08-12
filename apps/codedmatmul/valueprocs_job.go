package codedmatmul

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"gonum.org/v1/gonum/mat"

	"sigmaos/apps/codedmatmul/mdscode"
	db "sigmaos/debug"
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
	// The surplus the scheduler shed, which completed nothing and so appears in
	// no result. Its slabs are what shedding cost, and are the figure this arm
	// is meant to be judged on: an arm that stops its surplus early should show
	// less wasted work than one that lets it run, and neither claim is checkable
	// if the stopped attempts are simply absent from the accounting.
	if shed, err := j.shedSamples(); err != nil {
		return nil, nil, stats, err
	} else {
		samples = append(samples, shed...)
	}
	sort.Slice(samples, func(a, b int) bool { return samples[a].Idx < samples[b].Idx })

	C, err := mdscode.Decode(j.g, finishedBlocks)
	if err != nil {
		return nil, nil, stats, err
	}
	return C, samples, stats, nil
}

// shedSamples collects what the stopped attempts reported before they stopped.
//
// A stopped attempt hands its partial back to the scheduler, which surfaces it
// on the leaf; this is the only path by which it reaches the submitter, since
// the results stream carries completions alone. Every one of them is Used
// false: a shed worker's block is incomplete and never enters the decode, so
// all of its work is waste by construction.
//
// A leaf that reported nothing is skipped rather than counted as zero, so a
// worker stopped before its first slab is absent from the accounting instead of
// claiming to have cost nothing.
func (j *ValueProcsJob) shedSamples() ([]*Sample, error) {
	st, err := j.awaitSettled()
	if err != nil {
		return nil, err
	}
	var out []*Sample
	for _, n := range st.GetNodes() {
		if !n.GetIsLeaf() || len(n.GetPartial()) == 0 {
			continue
		}
		ps, err := proc.StatusFromBytes(n.GetPartial())
		if err != nil || ps == nil {
			// Unreadable partials are dropped rather than fatal: they cost the
			// accounting one worker's slabs, where failing the whole run would
			// cost the measurement entirely.
			db.DPrintf(db.CODEDMATMUL, "codedmatmul valueprocs: leaf %v partial undecodable: %v", n.GetNodeID(), err)
			continue
		}
		wr, err := NewWorkerResult(ps.Data())
		if err != nil {
			db.DPrintf(db.CODEDMATMUL, "codedmatmul valueprocs: leaf %v partial not a WorkerResult: %v", n.GetNodeID(), err)
			continue
		}
		out = append(out, &Sample{Idx: wr.Idx, WR: wr, Used: false})
	}
	return out, nil
}

// awaitSettled waits for the tree's stopped attempts to have actually ended,
// and returns the status once they have.
//
// Wait returns the moment the K-th result arrives, which is before the surplus
// has gone: the scheduler has only just asked those attempts to stop, and a
// stopped attempt's partial does not exist until it has run its shutdown and
// reported. Reading the tree straight away therefore finds no partials at all
// and silently reports the surplus as having cost nothing -- which is the same
// blind spot this was written to remove, in a form that looks like data.
//
// Charged is the right thing to wait on rather than a state of its own: it
// covers queued, running and stopping alike, so a leaf still holding a slot for
// any reason is a leaf whose account is not yet final.
//
// Bounded, because a proc that ignores its eviction must not hang the
// benchmark. On expiry the accounting is short by whatever that attempt did,
// which is the same failure as before and no worse.
func (j *ValueProcsJob) awaitSettled() (*proto.TreeStatusRep, error) {
	deadline := time.Now().Add(settleTimeout)
	for {
		st, err := j.c.Status(j.tid)
		if err != nil {
			return nil, fmt.Errorf("codedmatmul valueprocs: status: %w", err)
		}
		if st.GetNCharged() == 0 {
			return st, nil
		}
		if time.Now().After(deadline) {
			db.DPrintf(db.CODEDMATMUL, "codedmatmul valueprocs: %d attempts still charged after %v; their work is unaccounted",
				st.GetNCharged(), settleTimeout)
			return st, nil
		}
		time.Sleep(settlePoll)
	}
}

const (
	// settleTimeout bounds the wait for shed attempts to report. Generous
	// against an eviction round trip and short against a benchmark's patience.
	settleTimeout = 10 * time.Second
	settlePoll    = 50 * time.Millisecond
)

// Status returns the tree's raw status, for callers that want the full
// per-leaf breakdown (state, run count, stops, score) rather than just the
// aggregate NAttemptsStopped.
func (j *ValueProcsJob) Status() (*proto.TreeStatusRep, error) {
	return j.c.Status(j.tid)
}

// NAttemptsStopped reports how many attempts across the tree's leaves the
// scheduler stopped, summing each leaf's cumulative Stops, sampled right after
// Wait returns. It is the closest analog to Result.NEvicted, which valuesched
// does not report as such.
//
// It counts attempts and says nothing about what they cost; what they cost is
// in the slabs shedSamples collects.
//
// NCharged (TreeStatusRep's other count) is unsuitable for this: it is a
// live occupancy gauge, not a cumulative counter, so by the time Wait has
// returned and the tree has fully settled, it reads near zero regardless of
// how much surplus the scheduler actually shed along the way.

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
