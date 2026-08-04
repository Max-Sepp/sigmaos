package benchmarks_test

// TestMRValueProcs is TestMRStragglerBaseline/TestMRSpeculativeExecution's
// counterpart for the value-procs-scheduled coordinator (apps/mr/valueprocs_coord.go):
// same injected straggler, same workload, driven through a Select(nmap,
// Select(1, primary, dup)...) tree instead of ft/task's claim-based queue
// plus the baseline's time-heuristic backup logic.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"sigmaos/apps/mr"
	"sigmaos/benchmarks"
	db "sigmaos/debug"
	"sigmaos/ft/procgroupmgr"
	"sigmaos/proc"
	sp "sigmaos/sigmap"
	"sigmaos/test"
	"sigmaos/util/rand"
	"sigmaos/valueprocs/adapter"
	"sigmaos/valueprocs/clnt"
	"sigmaos/valueprocs/proto"
)

// ExpectedMRTaskDur is a rough per-task duration estimate fed to the
// coordinator (mr.VPConfig.ExpectedMapDur/ExpectedReduceDur), used only to
// compute Gradient (apps/mr/valueprocs_shared.go) -- it doesn't need to be
// accurate, only well below StragglerSlowdownMs, so the injected straggler's
// gradient clears the scheduler's GradientFloor quickly and its duplicate
// actually gets a chance to race it.
const ExpectedMRTaskDur = 5 * time.Second

// MRValueProcsJobInstance drives one MR job through the value-procs
// coordinator instead of the ft/task-based baseline. It reads the same job
// description as MRJobInstance but skips InitCoordFS/PrepareJob entirely --
// there is no ft/task service in this path, and the coordinator computes
// its own bins (see apps/mr/valueprocs_coord.go).
type MRValueProcsJobInstance struct {
	*test.RealmTstate
	app, jobRoot, jobname string
	memreq                proc.Tmem
	slowTaskId            int64
	slowdownMs            int
	job                   *mr.Job
	cm                    *procgroupmgr.ProcGroupMgr
}

func NewMRValueProcsJobInstance(ts *test.RealmTstate, app, jobRoot, jobname string, memreq proc.Tmem, slowTaskId int64, slowdownMs int) *MRValueProcsJobInstance {
	return &MRValueProcsJobInstance{
		RealmTstate: ts, app: app, jobRoot: jobRoot, jobname: jobname,
		memreq: memreq, slowTaskId: slowTaskId, slowdownMs: slowdownMs,
	}
}

func (ji *MRValueProcsJobInstance) PrepareMRJob() {
	jobf, err := mr.ReadJobConfig(filepath.Join("..", "apps/mr/job-descriptions", ji.app))
	assert.Nil(ji.Ts.T, err, "Error ReadJobConfig: %v", err)
	ji.job = mr.JobLocalToAny(jobf, true, true, true)
}

func (ji *MRValueProcsJobInstance) StartMRJob() {
	db.DPrintf(db.TEST, "Start MR value-procs job %v %v (straggler task %d +%dms)", ji.jobname, ji.job, ji.slowTaskId, ji.slowdownMs)
	vpcfg := mr.VPConfig{
		MemPerTask:        ji.memreq,
		ExpectedMapDur:    ExpectedMRTaskDur,
		ExpectedReduceDur: ExpectedMRTaskDur,
		MapperBin:         "mr-m-" + ji.job.App,
		ReducerBin:        "mr-r-" + ji.job.App,
	}
	cm, err := mr.StartValueProcsJob(ji.SigmaClnt, ji.jobRoot, ji.jobname, ji.job, vpcfg, ji.slowTaskId, ji.slowdownMs)
	assert.Nil(ji.Ts.T, err, "Error StartValueProcsJob: %v", err)
	ji.cm = cm
}

func (ji *MRValueProcsJobInstance) Wait() {
	mr.WaitJobDone(ji.FsLib, ji.jobRoot, ji.jobname)
}

// sumLeafStops sums how many times each leaf in a settled tree was stopped
// by the scheduler. It's a cumulative per-leaf counter, unlike
// TreeStatusRep.NCharged (a live occupancy gauge that reads near zero once a
// tree has fully settled, regardless of how much surplus the scheduler shed
// along the way) -- see codedmatmul.ValueProcsJob.NAttemptsStopped, which
// hit the same pitfall first.
func sumLeafStops(st *proto.TreeStatusRep) int32 {
	var n int32
	for _, node := range st.Nodes {
		if node.IsLeaf {
			n += node.Stops
		}
	}
	return n
}

// runMRValueProcsStragglerJob is runMRStragglerJob's counterpart for the
// value-procs coordinator: same app/memory/straggler-injection parameters,
// but no AStat snapshot to collect -- this coordinator never spawns a proc
// directly, so it has no wall-time bookkeeping of its own to report (see
// caveats in notes/design/valueprocs-plan.md's porting plan). Stopped
// attempts (sumLeafStops) are the closest available analog to Nspeculate,
// sampled from the scheduler directly rather than from the coordinator's
// own exit status.
func runMRValueProcsStragglerJob(mrts *test.MultiRealmTstate, vpc *clnt.Clnt, slowdownMs int) (dur time.Duration, mapStopped, reduceStopped int32) {
	ts := mrts.T
	rts := mrts.GetRealm(REALM1)

	jobname := StragglerMRApp + "-mr-vp-straggler-" + rand.String(3) + "-" + rts.GetRealm().String()
	ji := NewMRValueProcsJobInstance(rts, StragglerMRApp, chooseMRJobRoot(rts), jobname, proc.Tmem(StragglerMemReq), StragglerSlowTaskId, slowdownMs)
	ji.PrepareMRJob()

	start := time.Now()
	ji.StartMRJob()
	ji.Wait()
	dur = time.Since(start)
	ji.cm.WaitGroup()

	if st, err := vpc.Status(mr.ValueProcsMapTid(jobname)); err == nil {
		mapStopped = sumLeafStops(st)
	} else {
		db.DPrintf(db.ALWAYS, "runMRValueProcsStragglerJob: Status map tree err %v", err)
	}
	if st, err := vpc.Status(mr.ValueProcsReduceTid(jobname)); err == nil {
		reduceStopped = sumLeafStops(st)
	} else {
		db.DPrintf(db.ALWAYS, "runMRValueProcsStragglerJob: Status reduce tree err %v", err)
	}

	err := mr.PrintMRStats(ji.FsLib, ji.jobRoot, ji.jobname)
	assert.Nil(ts, err, "Error print MR stats: %v", err)

	return dur, mapStopped, reduceStopped
}

// TestMRValueProcs runs the same straggler-injected job as
// TestMRStragglerBaseline/TestMRSpeculativeExecution, but through the
// value-procs coordinator: every map task is submitted as an inner
// Select(1, primary, duplicate) node, and the scheduler -- not a
// SpecSlowFactor threshold -- decides whether the straggler's duplicate is
// worth starting, from the gradient its primary attempt reports as it runs
// well past ExpectedMRTaskDur. Compare its printed completion time against
// TestMRStragglerBaseline's (should be much closer to TestMRNoStraggler's,
// like TestMRSpeculativeExecution's).
func TestMRValueProcs(t *testing.T) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return
	}
	defer mrts.Shutdown()

	sc := mrts.GetRealm(REALM1).SigmaClnt
	vpjob := adapter.StartJob(sc, 0)
	defer vpjob.Stop()
	vpc := clnt.NewClnt(sc.FsLib)

	rs := benchmarks.NewResults(1, benchmarks.E2E)
	dur, mapStopped, reduceStopped := runMRValueProcsStragglerJob(mrts, vpc, StragglerSlowdownMs)
	rs.Append(dur, 1.0)
	printResultSummary(rs)

	db.DPrintf(db.ALWAYS, "MR value-procs straggler (task %d +%dms): completion time %v -- compare against TestMRStragglerBaseline's printed completion time", StragglerSlowTaskId, StragglerSlowdownMs, dur)
	db.DPrintf(db.ALWAYS, "MR value-procs: map tree attempts stopped %d, reduce tree attempts stopped %d -- an attempt count, not a wall-time-wasted figure like TestMRSpeculativeExecution's MsWasted", mapStopped, reduceStopped)
}
