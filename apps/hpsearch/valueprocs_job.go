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
//
// scale and silent are the negative-control knobs (see Config): a scale other
// than 1 inflates what this trainer reports without changing what it returns,
// and silent withholds its reports until it completes. Both are always passed
// so the argument list has one shape, and an honest trainer is just the
// (1, false) case of it.
func SpawnValueProcsTrainerLeaf(configId int, seed int64, maxIters int, iterDur time.Duration, mem proc.Tmem, scale float64, silent bool) *clnt.WorkNode {
	args := []string{
		strconv.Itoa(configId),
		strconv.FormatInt(seed, 10),
		strconv.Itoa(maxIters),
		strconv.FormatInt(iterDur.Milliseconds(), 10),
		strconv.FormatFloat(scale, 'g', -1, 64),
		strconv.FormatBool(silent),
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
		scale := 1.0
		if i == cfg.InflateConfig && cfg.InflateFactor > 0 {
			scale = cfg.InflateFactor
		}
		leaves[i] = SpawnValueProcsTrainerLeaf(i, seeds[i], cfg.MaxIters, cfg.IterDur, cfg.Mem, scale, i == cfg.SilentConfig)
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

// TrialOutcome is what became of one configuration: it either finished, or it
// was stopped before it could.
//
// Both are confirmed rather than inferred. Finished means this trial reported
// a complete curve of its own; stopped means the scheduler's own record shows
// it holding no run and having been stopped. Nothing here guesses which trial
// "won" -- that is the application's call, and Best is where it is made.
type TrialOutcome struct {
	ConfigId int

	// Finished is whether the trial reported its whole history before any stop
	// landed on it.
	//
	// More than one trial can finish. A Select(1, N) is satisfied the moment
	// one child completes, but stopping the rest is a request that takes time
	// to travel and be acted on, and a trial already on its last iteration can
	// finish inside that window. It raced the stop and won, and its result is
	// exactly as real as the first one's -- so discarding it, as an interface
	// that returns a single winner must, throws away work that was done and
	// paid for.
	Finished bool

	// Curve is this trial's real, full-fidelity history. Non-nil exactly when
	// Finished.
	Curve *Curve

	// LastScore is the last score the scheduler saw from a stopped trial, and
	// HasScore whether it ever reported one. Meaningless when Finished, where
	// Curve carries the whole history instead.
	LastScore float64
	HasScore  bool

	// Stops and Runs are the scheduler's own accounting for this leaf, which
	// is what makes "stopped" a confirmation rather than an assumption: a
	// trial that neither finished nor was ever stopped is a third state, and
	// one worth seeing rather than silently filing under either.
	Stops int
	Runs  uint64
}

// Wait blocks until the tree settles and returns what became of every
// configuration, in ConfigId order.
//
// The previous shape of this function returned one winner plus a reconstructed
// curve per loser, and it was wrong twice over. It took the winner by ranging
// over a map keyed by node id, so when more than one trial finished -- which
// happens whenever one races a stop and wins -- which of them was called the
// winner depended on Go's map iteration order, and changed run to run. And it
// then reported the survivors it had discarded as though they had been pruned.
//
// Selection belongs to the application anyway. The scheduler's job is to
// decide what runs; deciding which of the results that came back is best is a
// question about hyperparameters, not about capacity, and only the search
// knows how to answer it. So this hands back everything and Best chooses.
func (j *ValueProcsJob) Wait() ([]*TrialOutcome, error) {
	results, _, err := j.c.Wait(j.tid)
	if err != nil {
		return nil, err
	}
	st, err := j.c.Status(j.tid)
	if err != nil {
		return nil, err
	}

	out := make([]*TrialOutcome, j.cfg.NConfigs)
	for _, n := range st.Nodes {
		if !n.IsLeaf {
			continue
		}
		var configId int
		if _, err := fmt.Sscanf(n.Label, "config-%d", &configId); err != nil || configId < 0 || configId >= len(out) {
			continue
		}
		o := &TrialOutcome{
			ConfigId:  configId,
			LastScore: n.Score,
			HasScore:  n.HasScore,
			Stops:     int(n.Stops),
			Runs:      n.Run,
		}
		if res, ok := results[n.NodeID]; ok {
			c, err := NewCurve(res.Status.Data())
			if err != nil {
				return nil, fmt.Errorf("hpsearch valueprocs: decode config %d: %w", configId, err)
			}
			o.Finished, o.Curve = true, c
		}
		out[configId] = o
	}

	for i, o := range out {
		if o == nil {
			return nil, fmt.Errorf("hpsearch valueprocs: tree %v reported nothing for config %d", j.tid, i)
		}
	}
	return out, nil
}

// Best is the application's selection: the finished trial with the highest
// final score.
//
// Only finished trials are eligible. A stopped trial's last reported score is
// a lower bound on where it would have ended up, not an estimate of it, so
// preferring one over a trial that actually ran to completion would be
// choosing a config on the strength of a number that was never comparable to
// the one it is being compared against.
//
// Returns nil if nothing finished, which is a broken run rather than a search
// with no answer.
func Best(outcomes []*TrialOutcome) *TrialOutcome {
	var best *TrialOutcome
	for _, o := range outcomes {
		if !o.Finished || o.Curve == nil {
			continue
		}
		if best == nil || curveQuality(o.Curve) > curveQuality(best.Curve) {
			best = o
		}
	}
	return best
}

// curveQuality is what a trial is judged on: the best score it reached,
// matching how BestQuality scores a whole run.
func curveQuality(c *Curve) float64 {
	return BestScore(c.Scores, len(c.Scores))
}

// NFinished is how many trials ran to completion. One is the ordinary case;
// more than one means that many raced a stop and won.
func NFinished(outcomes []*TrialOutcome) int {
	n := 0
	for _, o := range outcomes {
		if o.Finished {
			n++
		}
	}
	return n
}

// Curves renders outcomes as the curve list Analyze and AnalyzeLive consume:
// the real history for every trial that finished, and a reconstruction for
// every trial that was stopped.
//
// The reconstruction is only ever used to account for compute already spent,
// never to select. valuesched has no way for a submitter to read back what a
// stopped leaf was doing -- only that leaf's own next attempt could, via
// ResumeToken, and there is no next attempt here -- so a stopped trial's
// progress is recovered from its seed: syntheticCurve is deterministic, so
// regenerating the full curve and truncating it where the last score the
// scheduler saw best matches recovers approximately the right length.
//
// It is approximate in two ways. Score pushes are coalesced (vproc's default
// ~100ms interval), so the matched iteration can be off by one or two, and
// near the asymptote noise can make neighbouring iterations indistinguishable.
// Which trials finished is exact; how far the stopped ones got is not.
func (j *ValueProcsJob) Curves(outcomes []*TrialOutcome) []*Curve {
	curves := make([]*Curve, 0, len(outcomes))
	for _, o := range outcomes {
		if o.Finished && o.Curve != nil {
			curves = append(curves, o.Curve)
			continue
		}
		curves = append(curves, reconstructLoserCurve(o.ConfigId, j.seeds[o.ConfigId], j.cfg.MaxIters, o.LastScore, o.HasScore))
	}
	return curves
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
