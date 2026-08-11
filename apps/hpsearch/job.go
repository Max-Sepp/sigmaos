package hpsearch

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/sigmaclnt"
	sp "sigmaos/sigmap"
)

const (
	TrainerBin        = "hp-trainer"
	PruningTrainerBin = "hp-trainer-pruned"

	ProgressDirTop = sp.NAMED + "hpsearch-progress/"

	// PruneMargin is how far behind the best config's score a config must
	// fall (and stay) before it is pruned.
	PruneMargin = 0.1
)

// Config is one hyperparameter search: how many configs to try and how long
// to run each.
//
// No arm declares a resource reservation. A trainer's real footprint is one
// busy core and negligible memory, but declaring either would put the
// baseline and pruning arms under besched's admission accounting while the
// value-procs arm, whose leaves declare nothing, stays outside it -- so the
// arms would differ in how they are admitted as well as in how they are
// scheduled, which is the one thing a comparison between them must not
// confound.
type Config struct {
	NConfigs int
	MaxIters int
	IterDur  time.Duration
	Seed     int64
	Margin   float64

	// Negative controls for the value-procs arm: one config can be made to
	// misreport, so the cost of a trial the scheduler cannot trust is
	// measurable rather than assumed.
	//
	// Both name a config rather than a fraction of them, because the question
	// is what a single bad neighbour does to the honest ones -- and because a
	// tree in which everybody lies is a tree in which the ranking is
	// unchanged, so it tests nothing. -1 disables.
	//
	// Neither knob touches the Curve a trainer returns. The scheduler's view
	// is corrupted; the ground truth Analyze measures quality against is not,
	// which is what lets a test see the honest configs lose.
	InflateConfig int     // config that multiplies its reported score by InflateFactor
	InflateFactor float64 // how much it inflates by; 1 is honest
	SilentConfig  int     // config that reports nothing at all until it completes
}

func DefaultConfig() *Config {
	return &Config{
		NConfigs: 15,
		MaxIters: 300,
		IterDur:  50 * time.Millisecond,
		Seed:     7159623, // Fixed to make the synthetic curves reproducible
		Margin:   PruneMargin,

		InflateConfig: -1,
		InflateFactor: 1,
		SilentConfig:  -1,
	}
}

// HPSearchJob represents a hyperparameter search job. The trainers have been
// spawned but have not yet necessarily been started or exited. Callers can
// inspect the trainers and wait for them to finish with Wait.
type HPSearchJob struct {
	sc    *sigmaclnt.SigmaClnt
	cfg   *Config
	procs []*proc.Proc
	// progressDir is where the pruning trainers publish their scores; empty
	// for a baseline job, which shares nothing.
	progressDir string
	waitedOn    bool
}

// Procs returns the job's trainer procs, in configId order, so a caller can
// inspect or evict them between StartNoPruneJob and Wait.
func (j *HPSearchJob) Procs() []*proc.Proc {
	return j.procs
}

// Only needed to observe a job's pruning decisions while it runs.
func (j *HPSearchJob) ProgressDir() string {
	return j.progressDir
}

func (j *HPSearchJob) WaitStart() error {
	for _, p := range j.procs {
		if err := j.sc.WaitStart(p.GetPid()); err != nil {
			return err
		}
	}
	return nil
}

// Wait blocks until every trainer has exited and returns each config's
// learning curve, in configId order.
//
// Should only be called once per job since it cleans up a job's resources
// (like the progress directory) and marks the job as waited on.
func (j *HPSearchJob) Wait() ([]*Curve, error) {
	if j.waitedOn {
		return nil, fmt.Errorf("Wait called more than once on this job")
	}
	j.waitedOn = true

	// Clean up the shared progress directory, if this job has one, once
	// every trainer is done with it. Failing to remove it doesn't
	// invalidate the curves, so just log it.
	if j.progressDir != "" {
		defer func() {
			if err := j.sc.RmDir(j.progressDir); err != nil {
				db.DPrintf(db.HPSEARCH, "HPSearchJob.Wait: RmDir %v err %v", j.progressDir, err)
			}
		}()
	}
	return WaitJobExit(j.sc, j.procs)
}

// SpawnTrainer spawns a single hp-trainer proc for one hyperparameter
// configuration, without waiting for it to start running.
func SpawnTrainer(sc *sigmaclnt.SigmaClnt, configId int, seed int64, maxIters int, iterDur time.Duration) (*proc.Proc, error) {
	args := []string{
		strconv.Itoa(configId),
		strconv.FormatInt(seed, 10),
		strconv.Itoa(maxIters),
		strconv.FormatInt(iterDur.Milliseconds(), 10),
	}

	p := proc.NewProc(TrainerBin, args)

	if err := sc.Spawn(p); err != nil {
		return nil, err
	}
	return p, nil
}

// StartTrainer spawns a single hp-trainer proc and waits for it to start
// running.
func StartTrainer(sc *sigmaclnt.SigmaClnt, configId int, seed int64, maxIters int, iterDur time.Duration) (*proc.Proc, error) {
	p, err := SpawnTrainer(sc, configId, seed, maxIters, iterDur)
	if err != nil {
		return nil, err
	}
	if err := sc.WaitStart(p.GetPid()); err != nil {
		return nil, err
	}
	return p, nil
}

func StartNoPruneJob(sc *sigmaclnt.SigmaClnt, cfg *Config) (*HPSearchJob, error) {
	// Random number generator for seeds
	rng := rand.New(rand.NewSource(cfg.Seed))

	procs := make([]*proc.Proc, cfg.NConfigs)
	// Spawn one trainer per config, each with its own derived seed.
	for i := 0; i < cfg.NConfigs; i++ {
		p, err := SpawnTrainer(sc, i, rng.Int63(), cfg.MaxIters, cfg.IterDur)
		if err != nil {
			return nil, err
		}
		procs[i] = p
	}
	return &HPSearchJob{sc: sc, cfg: cfg, procs: procs, waitedOn: false}, nil
}

func SpawnPruningTrainer(sc *sigmaclnt.SigmaClnt, configId int, seed int64, maxIters int, iterDur time.Duration, progressDir string, margin float64) (*proc.Proc, error) {
	// Same argv as SpawnTrainer, plus the progressDir/margin the trainer
	// needs to prune itself against its siblings.
	args := []string{
		strconv.Itoa(configId),
		strconv.FormatInt(seed, 10),
		strconv.Itoa(maxIters),
		strconv.FormatInt(iterDur.Milliseconds(), 10),
		progressDir,
		strconv.FormatFloat(margin, 'f', -1, 64),
	}
	p := proc.NewProc(PruningTrainerBin, args)
	if err := sc.Spawn(p); err != nil {
		return nil, err
	}
	return p, nil
}

// StartPruningTrainer spawns a single hp-trainer-pruned proc and waits for
// it to start running.
func StartPruningTrainer(sc *sigmaclnt.SigmaClnt, configId int, seed int64, maxIters int, iterDur time.Duration, progressDir string, margin float64) (*proc.Proc, error) {
	p, err := SpawnPruningTrainer(sc, configId, seed, maxIters, iterDur, progressDir, margin)
	if err != nil {
		return nil, err
	}
	if err := sc.WaitStart(p.GetPid()); err != nil {
		return nil, err
	}
	return p, nil
}

func StartPruningJob(sc *sigmaclnt.SigmaClnt, cfg *Config) (*HPSearchJob, error) {
	// Give this run its own uniquely-named progress directory.
	progressDir := ProgressDirTop + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := sc.MkDirPath(sp.NAMED, strings.TrimPrefix(progressDir, sp.NAMED), 0777); err != nil {
		return nil, err
	}

	// Random number generator for seeds
	rng := rand.New(rand.NewSource(cfg.Seed))

	procs := make([]*proc.Proc, cfg.NConfigs)
	// Spawn one pruning trainer per config, all sharing progressDir.
	for i := 0; i < cfg.NConfigs; i++ {
		p, err := SpawnPruningTrainer(sc, i, rng.Int63(), cfg.MaxIters, cfg.IterDur, progressDir, cfg.Margin)
		if err != nil {
			return nil, err
		}
		procs[i] = p
	}
	return &HPSearchJob{sc: sc, cfg: cfg, procs: procs, progressDir: progressDir, waitedOn: false}, nil
}

// WaitJobExit waits for every trainer proc of a job to exit and returns each
// config's resulting learning curve, in the order the procs were spawned.
func WaitJobExit(sc *sigmaclnt.SigmaClnt, procs []*proc.Proc) ([]*Curve, error) {
	curves := make([]*Curve, len(procs))
	for i, p := range procs {
		// Block until this config's trainer exits.
		status, err := sc.WaitExit(p.GetPid())
		if err != nil {
			return nil, err
		}

		// A non-OK status means the trainer crashed rather than reporting a
		// curve
		if !status.IsStatusOK() {
			return nil, fmt.Errorf("hp-trainer %v exited with non-OK status %v: %v", p.GetPid(), status.StatusCode, status.Msg())
		}
		// Decode the Curve back out of the generic status data.
		curve, err := NewCurve(status.Data())
		if err != nil {
			return nil, err
		}
		curves[i] = curve
	}
	return curves, nil
}
