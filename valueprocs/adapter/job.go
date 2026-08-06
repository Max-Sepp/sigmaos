package adapter

import (
	db "sigmaos/debug"
	"sigmaos/ft/procgroupmgr"
	"sigmaos/proc"
	"sigmaos/sigmaclnt"
	"sigmaos/valueprocs"
	"sigmaos/valueprocs/policy"
)

// Job runs the scheduling service under supervision.
type Job struct {
	pgm *procgroupmgr.ProcGroupMgr
}

// StartJob spawns the service and keeps it running.
//
// One instance per realm, which is also the whole of the isolation story: a
// score means something only among the children of one node, and nothing at
// all between tenants, so there is no configuration that could accidentally
// compare them.
//
// procgroupmgr restarts the service if it dies, and every restart mints a
// fresh proc and bumps the generation it passes in. The service turns that
// into its epoch, which is how a client discovers its results are gone rather
// than waiting forever for the next one.
func StartJob(sc *sigmaclnt.SigmaClnt, mcpu proc.Tmcpu) *Job {
	return StartJobSignal(sc, mcpu, policy.SignalValue)
}

// StartJobSignal is StartJob running the service under a named policy signal.
//
// It exists for the arm that ablates the signal: the same service, the same
// trees, the same admission machinery, deciding on occupancy alone (see
// policy.Signal). Passing the mode as a proc argument rather than baking it
// into the binary is what lets one deployment serve both arms, so a comparison
// between them does not also compare two builds.
func StartJobSignal(sc *sigmaclnt.SigmaClnt, mcpu proc.Tmcpu, sig policy.Signal) *Job {
	cfg := procgroupmgr.NewProcGroupConfig(1, "valuesched", []string{sig.String()}, mcpu, valueprocs.VALUESCHEDREL)
	db.DPrintf(db.VALUEPROC, "starting valuesched signal %v", sig)
	return &Job{pgm: cfg.StartGrpMgr(sc)}
}

// Stop shuts the service down.
//
// Work already running does not stop with it. Whether an application's trees
// should outlive the scheduler is the application's business, and a service
// that killed them every time it restarted would make restarting worse than
// staying down. Cancel first if that is not what you want.
func (j *Job) Stop() ([]*procgroupmgr.ProcStatus, error) {
	db.DPrintf(db.VALUEPROC, "stopping valuesched")
	return j.pgm.StopGroup()
}
