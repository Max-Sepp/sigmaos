package hpsearch

import (
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"time"

	"sigmaos/proc"
	sp "sigmaos/sigmap"
	"sigmaos/valueprocs/clnt"
	"sigmaos/valueprocs/proto"
)

// TrainerVPBin is the value-procs-scheduled counterpart of TrainerBin.
const TrainerVPBin = "hp-trainer-vp"

// SpawnValueProcsTrainerLeaf builds (but does not spawn) one config's
// WorkNode. Unlike SpawnTrainer, this proc reserves no mcpu.
func SpawnValueProcsTrainerLeaf(configId int, seed int64, maxIters int, iterDur time.Duration, mem proc.Tmem) *clnt.WorkNode {
	args := []string{
		strconv.Itoa(configId),
		strconv.FormatInt(seed, 10),
		strconv.Itoa(maxIters),
		strconv.FormatInt(iterDur.Milliseconds(), 10),
	}
	p := proc.NewProc(TrainerVPBin, args)
	if mem > 0 {
		p.SetMem(mem)
	}
	return clnt.Leaf(p).WithLabel(fmt.Sprintf("config-%d", configId))
}

// ValueProcsJob is StartNoPruneJob/StartPruningJob's counterpart, driving
// the same hyperparameter search through valuesched as a Select(1, N) tree:
// keep the best of NConfigs trials, pruning the rest as pressure rises.
type ValueProcsJob struct {
	c     *clnt.Clnt
	cfg   *Config
	tid   string
	seeds []int64
}

// StartValueProcsJob submits a Select(1, w0..w{NConfigs-1}) tree, one leaf
// per hyperparameter configuration. Unlike StartPruningJob, the per-config
// seeds are retained on the job (not just handed to the trainers), because
// they are what let Wait reconstruct an approximate curve for every config
// the scheduler pruned -- see Wait's doc comment.
func StartValueProcsJob(c *clnt.Clnt, cfg *Config) (*ValueProcsJob, error) {
	rng := rand.New(rand.NewSource(cfg.Seed))

	seeds := make([]int64, cfg.NConfigs)
	leaves := make([]*clnt.WorkNode, cfg.NConfigs)
	for i := 0; i < cfg.NConfigs; i++ {
		seeds[i] = rng.Int63()
		leaves[i] = SpawnValueProcsTrainerLeaf(i, seeds[i], cfg.MaxIters, cfg.IterDur, cfg.Mem)
	}
	root, err := clnt.Select(1, leaves...)
	if err != nil {
		return nil, err
	}

	tid := "hpsearch-" + sp.GenPid("").String()
	if _, err := c.Submit(tid, "hpsearch", root); err != nil {
		return nil, err
	}
	return &ValueProcsJob{c: c, cfg: cfg, tid: tid, seeds: seeds}, nil
}

// Wait blocks until the tree settles and returns the winning trial's curve
// (full fidelity -- the winner reported its whole history via Complete) plus
// an approximate curve for every config the scheduler pruned.
//
// A pruned config never reports a result at all: valuesched has no way for
// a submitter to retrieve what a stopped leaf was doing (only that leaf's
// own next attempt could see it, via ResumeToken, and there is no next
// attempt here). So losers are reconstructed instead of read back: this job
// already knows every config's seed, and syntheticCurve is deterministic,
// so regenerating a config's full curve locally and truncating it where its
// last-known reported score best matches recovers the same shape
// AnalyzeLive expects, approximately.
//
// The approximation has two sources of error: Score pushes are coalesced
// (vproc's default ~100ms interval), so the matched iteration may be off by
// one or two, and near the curve's asymptote, noise can make two different
// iterations' scores nearly indistinguishable. NPruned itself is exact
// (Select(1,N) guarantees exactly one winner); PrunedAtIter/derived
// core-seconds are not.
func (j *ValueProcsJob) Wait() (winner *Curve, approxLosers []*Curve, err error) {
	results, _, err := j.c.Wait(j.tid)
	if err != nil {
		return nil, nil, err
	}
	var winnerNodeID string
	for _, res := range results {
		winnerNodeID = res.NodeID
		winner, err = NewCurve(res.Status.Data())
		if err != nil {
			return nil, nil, fmt.Errorf("hpsearch valueprocs: decode winner: %w", err)
		}
	}
	if winner == nil {
		return nil, nil, fmt.Errorf("hpsearch valueprocs: tree %v settled with no winner", j.tid)
	}

	st, err := j.c.Status(j.tid)
	if err != nil {
		return nil, nil, err
	}
	approxLosers = make([]*Curve, 0, j.cfg.NConfigs-1)
	for _, n := range st.Nodes {
		if !n.IsLeaf || n.NodeID == winnerNodeID {
			continue
		}
		var configId int
		if _, err := fmt.Sscanf(n.Label, "config-%d", &configId); err != nil || configId < 0 || configId >= len(j.seeds) {
			continue
		}
		approxLosers = append(approxLosers, reconstructLoserCurve(configId, j.seeds[configId], j.cfg.MaxIters, n.Score, n.HasScore))
	}
	return winner, approxLosers, nil
}

// Status returns the tree's raw status, for callers that want the full
// per-leaf breakdown (state, run count, stops, score) that Wait's
// reconstructed approxLosers curves only partially capture.
func (j *ValueProcsJob) Status() (*proto.TreeStatusRep, error) {
	return j.c.Status(j.tid)
}

// reconstructLoserCurve regenerates a pruned config's full deterministic
// curve and truncates it at the iteration whose score best matches the last
// one the scheduler saw, approximating where the trainer actually stopped.
func reconstructLoserCurve(configId int, seed int64, maxIters int, lastScore float64, hasScore bool) *Curve {
	asymptote, scores := syntheticCurve(seed, maxIters)
	prunedAtIter := 0
	if hasScore {
		best := math.Inf(1)
		for i, s := range scores {
			if d := math.Abs(s - lastScore); d < best {
				best, prunedAtIter = d, i
			}
		}
	}
	return &Curve{
		ConfigId:     configId,
		Seed:         seed,
		Asymptote:    asymptote,
		Scores:       scores[:prunedAtIter+1],
		Pruned:       true,
		PrunedAtIter: prunedAtIter + 1,
	}
}
