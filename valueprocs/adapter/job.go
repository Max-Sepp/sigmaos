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
	return StartJobArm(sc, mcpu, policy.SignalValue, policy.SizingHeadroom)
}

// StartJobSignal is StartJob running the service under a named policy signal,
// sizing on the ledger as the default arm does.
func StartJobSignal(sc *sigmaclnt.SigmaClnt, mcpu proc.Tmcpu, sig policy.Signal) *Job {
	return StartJobArm(sc, mcpu, sig, policy.SizingHeadroom)
}

// StartJobArm is StartJob running the service under a named arm.
//
// An arm is a signal and a sizing rule together: what the application is allowed
// to tell the scheduler, and what the cluster is allowed to tell it (see
// policy.Signal and policy.Sizing). Passing both as proc arguments rather than
// baking them into the binary is what lets one deployment serve every arm, so a
// comparison between them does not also compare builds.
func StartJobArm(sc *sigmaclnt.SigmaClnt, mcpu proc.Tmcpu, sig policy.Signal, sz policy.Sizing) *Job {
	args := []string{sig.String(), sz.String()}
	cfg := procgroupmgr.NewProcGroupConfig(1, "valuesched", args, mcpu, valueprocs.VALUESCHEDREL)
	db.DPrintf(db.VALUEPROC, "starting valuesched signal %v sizing %v", sig, sz)
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
