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

// Gradient is the marginal-value signal a mapper/reducer reports alongside
// its score: zero until a task has run its expected duration, then rising
// linearly and uncapped. It is a continuous generalization of the
// coordinator's existing SpecSlowFactor heuristic (elapsed > 1.5x mean),
// letting the scheduler's own GradientFloor decide whether a task is worth
// racing rather than reimplementing a threshold here.
//
// expected <= 0 means no estimate was given (e.g. this proc wasn't spawned
// by the value-procs coordinator), so the gradient is always 0 -- no racing
// signal, not a division by zero.
func Gradient(elapsed, expected time.Duration) float64 {
	if expected <= 0 {
		return 0
	}
	return math.Max(0, elapsed.Seconds()/expected.Seconds()-1)
}

// startVProc wires a mapper/reducer proc into the value-procs runtime. It
// never fails the caller: if this proc wasn't spawned by valuesched,
// vproc.StartWith's Score calls are silent no-ops, and if StartWith itself
// errors, scoring is simply skipped rather than failing the task -- calling
// sc.Started() a second time here (newMapper/RunReducer already called it
// once) is documented as safe (sched/msched/srv/procmgr/state.go's
// (*ProcState).started: "May be called multiple times").
func startVProc(sc *sigmaclnt.SigmaClnt) *vproc.Ctx {
	c, err := vproc.StartWith(sc)
	if err != nil {
		db.DPrintf(db.MR, "startVProc: StartWith err %v (continuing without value-procs scoring)", err)
		return nil
	}
	return c
}
