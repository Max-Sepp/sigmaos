package adapter

import (
	db "sigmaos/debug"
	"sigmaos/ft/procgroupmgr"
	"sigmaos/proc"
	"sigmaos/sigmaclnt"
	"sigmaos/valueprocs"
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
	cfg := procgroupmgr.NewProcGroupConfig(1, "valuesched", nil, mcpu, valueprocs.VALUESCHEDREL)
	db.DPrintf(db.VALUEPROC, "starting valuesched")
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
