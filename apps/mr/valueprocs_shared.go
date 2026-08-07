package mr

import (
	"math"
	"time"

	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/sigmaclnt"
	"sigmaos/valueprocs/vproc"
)

// InlineInputSentinel in a reducer's args (in place of an ft/task service
// id) means its input Bin is given inline instead: the value-procs
// coordinator has no ft/task service to read it back from.
const InlineInputSentinel = "-"

// VPConfig is one value-procs-scheduled MapReduce run: what memory each
// mapper/reducer gets, and how long a task is expected to take before its
// gradient starts rising -- see Gradient. NMap is computed by the
// coordinator itself from the input directory (see NewBins), unlike
// StartMRJob's baseline counterpart, which needs it passed in because a
// separate PrepareJob call already computed it to submit tasks into ft/task.
type VPConfig struct {
	MemPerTask                        proc.Tmem
	ExpectedMapDur, ExpectedReduceDur time.Duration
	MapperBin, ReducerBin             string
}

// Rate tracks the gradient a mapper/reducer reports alongside its score: the
// slope of its value curve, in score per expected-duration.
//
// The scheduler extrapolates along this. It reads score + gradient*w as the
// task's own estimate of where it will be w expected-durations later, so the
// number has to be a rate and not a measure of lateness -- substituting one
// that rises with overdueness would claim a stalled task is about to finish
// sooner than a healthy one. A task covering its work as fast as it predicted
// reports about 1, and one making no progress reports 0 however long it has
// been running.
//
// The slope is measured over the interval between reports rather than averaged
// from the start, because what matters is the curve where the task is now. A
// task that ran normally and then stalled has a respectable average and a
// current slope of zero, and it is the zero that should get it raced.
type Rate struct {
	expected time.Duration

	lastScore float64
	lastAt    time.Time
}

// NewRate returns a tracker for a task expected to take d.
//
// d <= 0 means no estimate was given -- this proc was not spawned by the
// value-procs coordinator -- and every gradient is then 0. That is the honest
// answer rather than a division by zero: with no expected duration there is no
// scale to normalize against, and 0 claims only that no progress can be
// accounted for.
func NewRate(d time.Duration) *Rate {
	return &Rate{expected: d}
}

// Observe records a score at a moment and returns the gradient to report with
// it.
func (r *Rate) Observe(score float64, now time.Time) float64 {
	if r.expected <= 0 {
		return 0
	}
	prevScore, prevAt := r.lastScore, r.lastAt
	r.lastScore, r.lastAt = score, now
	if prevAt.IsZero() {
		// The first report has no interval behind it to measure a slope over.
		// Nothing has been observed to happen yet, so nothing is claimed.
		return 0
	}
	dt := now.Sub(prevAt)
	if dt <= 0 {
		return 0
	}
	// Normalizing by expected duration is what makes this comparable with the
	// gradient of a task of another size, which is the whole reason the
	// scheduler can borrow one task's slope to value another.
	dtau := dt.Seconds() / r.expected.Seconds()
	return math.Max(0, (score-prevScore)/dtau)
}

// startVProc wires a mapper/reducer proc into the value-procs runtime. It
// never fails the caller: if this proc wasn't spawned by valuesched,
// vproc.StartWith's Score calls are silent no-ops, and if StartWith itself
// errors, scoring is simply skipped rather than failing the task -- calling
// sc.Started() a second time here (newMapper/RunReducer already called it
// once) is documented as safe (sched/msched/srv/procmgr/state.go's
// (*ProcState).started: "May be called multiple times").
//
// Scoring is all a mapper or reducer does with the runtime, so that is all it
// is handed. The nil on the error path is an untyped one, and has to stay
// that way: a nil *vproc.Ctx returned as a Scorer would be an interface the
// callers' "did valuesched spawn me" checks read as present.
func startVProc(sc *sigmaclnt.SigmaClnt) vproc.Scorer {
	c, err := vproc.StartWith(sc)
	if err != nil {
		db.DPrintf(db.MR, "startVProc: StartWith err %v (continuing without value-procs scoring)", err)
		return nil
	}
	return c
}
