package mr

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	db "sigmaos/debug"
	"sigmaos/ft/procgroupmgr"
	"sigmaos/proc"
	"sigmaos/sigmaclnt"
	"sigmaos/sigmaclnt/fslib"
	sp "sigmaos/sigmap"
	"sigmaos/valueprocs/clnt"
)

// The value-procs-scheduled counterpart of Coord: one Select(nmap, ...)
// tree for the map phase, one Select(nreduce, ...) tree for the reduce
// phase, each task wrapped in an inner Select(1, primary, duplicate) node so
// the scheduler may race a straggler by starting its duplicate -- see
// notes/design/valueprocs-plan.md §2. There is no ft/task queue in this
// path: valuesched owns admission for every attempt from the start, so the
// coordinator computes bins itself (NewBins) rather than reading them back
// from a task service, and passes a reducer its input Bin inline rather
// than by reference (see InlineInputSentinel).
//
// Full crash-recovery parity with the baseline coordinator (durable WIP-task
// bookkeeping across a coordinator crash) is deliberately out of scope: this
// path compares scheduling mechanisms under a fixed workload, not fault
// injection. What crash tolerance it does have comes for free from
// Submit's idempotency (a restarted coordinator resubmits the same
// deterministic tid and gets created=false back) and clnt.Next/Results
// being replay-safe from the beginning.

// prepareValueProcsDirs sets up a job's output/intermediate directories and
// links. It is PrepareJob's counterpart minus the bin enumeration and
// ft/task submission: RunValueProcsCoord computes bins itself once it's
// running, instead of a separate step submitting them ahead of time.
func prepareValueProcsDirs(fsl *fslib.FsLib, jobRoot, jobName string, j *Job) error {
	job := JobLocalToAny(j, false, false, true)
	if job.Output == "" || job.Intermediate == "" {
		return fmt.Errorf("mr valueprocs: job output (%q) or intermediate (%q) not supplied", job.Output, job.Intermediate)
	}
	fsl.MkDir(MRDIRTOP, 0777)
	fsl.MkDir(jobRoot, 0777)
	if err := fsl.MkDir(JobDir(jobRoot, jobName), 0777); err != nil {
		return err
	}
	if err := InitJobSem(fsl, jobRoot, jobName); err != nil {
		return err
	}

	fsl.MkDir(job.Output, 0777)
	outDir := JobOut(job.Output, jobName)
	if err := fsl.MkDir(outDir, 0777); err != nil {
		return err
	}
	if _, err := fsl.PutFile(JobOutLink(jobRoot, jobName), 0777, sp.OWRITE, []byte(job.Output)); err != nil {
		return err
	}

	// If intermediate output lives in S3, make it only once -- mappers make
	// intermediate and out dirs in their local ux themselves.
	if strings.Contains(job.Intermediate, "/s3/") {
		intOutDir := MapIntermediateDir(jobName, job.Intermediate)
		if err := fsl.MkDir(job.Intermediate, 0777); err != nil {
			return err
		}
		if err := fsl.MkDir(intOutDir, 0777); err != nil {
			return err
		}
	}
	if _, err := fsl.PutFile(JobIntOutLink(jobRoot, jobName), 0777, sp.OWRITE, []byte(job.Intermediate)); err != nil {
		return err
	}
	return nil
}

// StartValueProcsJob sets up a job's directories and spawns the mr-coord-vp
// proc under procgroupmgr, the value-procs-scheduled counterpart of
// StartMRJob. slowTaskId/slowdownMs inject a straggler exactly as they do
// for the baseline coordinator, but only into a task's primary attempt --
// its duplicate is never delayed, or racing it would mean nothing.
//
// NewProcGroupConfig prepends jobName to whatever args slice it's given
// (see ft/procgroupmgr/procgroupmgr.go's NewProcGroupConfigRealmSwitch), so
// jobName is deliberately NOT repeated in the slice below -- the coordinator
// reads it back as args[0], jobRoot as args[1], exactly like NewCoord does
// for the baseline (StartMRJob's own args slice starts with jobRoot too).
func StartValueProcsJob(sc *sigmaclnt.SigmaClnt, jobRoot, jobName string, j *Job, vpcfg VPConfig, slowTaskId int64, slowdownMs int) (*procgroupmgr.ProcGroupMgr, error) {
	if err := prepareValueProcsDirs(sc.FsLib, jobRoot, jobName, j); err != nil {
		return nil, err
	}
	job := JobLocalToAny(j, false, false, true)
	cfg := procgroupmgr.NewProcGroupConfig(NCOORD, "mr-coord-vp", []string{
		jobRoot,
		vpcfg.MapperBin, vpcfg.ReducerBin,
		strconv.Itoa(job.Linesz), strconv.Itoa(job.Wordsz),
		strconv.Itoa(job.Nreduce),
		strconv.Itoa(int(vpcfg.MemPerTask)),
		strconv.FormatInt(vpcfg.ExpectedMapDur.Milliseconds(), 10),
		strconv.FormatInt(vpcfg.ExpectedReduceDur.Milliseconds(), 10),
		strconv.FormatInt(slowTaskId, 10),
		strconv.Itoa(slowdownMs),
		job.Input,
		strconv.Itoa(job.Binsz),
	}, 1000, jobName)
	return cfg.StartGrpMgr(sc), nil
}

// mapperLeaf builds one map task attempt's WorkNode. idx labels it ("mI"),
// so whichever of a task's {primary, duplicate} attempts wins can be told
// apart from every other task's once results come back.
func mapperLeaf(mapperbin, jobRoot, job string, nreduce int, binJSON, intOutdir, linesz, wordsz string, slowdownMs, expectedMapDurMs int, mem proc.Tmem, idx int) *clnt.WorkNode {
	args := []string{
		jobRoot, job, strconv.Itoa(nreduce), binJSON, intOutdir, linesz, wordsz,
		strconv.Itoa(slowdownMs), strconv.Itoa(expectedMapDurMs),
	}
	p := proc.NewProc(mapperbin, args)
	if mem > 0 {
		p.SetMem(mem)
	}
	return clnt.Leaf(p).WithLabel(fmt.Sprintf("m%d", idx))
}

// reducerLeaf is mapperLeaf's mirror for the reduce phase. Its input Bin
// travels inline (InlineInputSentinel in place of an ft/task service id),
// since there is no task service for the reducer to read it back from.
func reducerLeaf(reducerbin string, idx int, outlink, outTarget string, nmap int, binJSON string, expectedReduceDurMs int, mem proc.Tmem) *clnt.WorkNode {
	args := []string{
		strconv.Itoa(idx), InlineInputSentinel, outlink, outTarget,
		strconv.Itoa(nmap), binJSON, strconv.Itoa(expectedReduceDurMs),
	}
	p := proc.NewProc(reducerbin, args)
	if mem > 0 {
		p.SetMem(mem)
	}
	return clnt.Leaf(p).WithLabel(fmt.Sprintf("r%d", idx))
}

// taskPair builds one task's inner Select(1, primary, duplicate) node.
func taskPair(primary, dup *clnt.WorkNode) (*clnt.WorkNode, error) {
	return clnt.Select(1, primary, dup)
}

// ValueProcsMapTid and ValueProcsReduceTid are the deterministic tids
// RunValueProcsCoord submits its two phases under -- exported so a caller
// (e.g. a benchmark wanting SchedStats/TreeStatus for the tree) doesn't have
// to duplicate the naming convention.
func ValueProcsMapTid(jobName string) string    { return "mr-map-" + jobName }
func ValueProcsReduceTid(jobName string) string { return "mr-reduce-" + jobName }

// labelIndex parses the integer suffix WithLabel(prefix+"N") gave a leaf,
// back out of a settled Result -- the only way this coordinator has to tell
// which task a result belongs to, since node identity itself is opaque.
func labelIndex(label, prefix string) (int, error) {
	s, ok := strings.CutPrefix(label, prefix)
	if !ok {
		return 0, fmt.Errorf("mr valueprocs: label %q missing prefix %q", label, prefix)
	}
	return strconv.Atoi(s)
}

// RunValueProcsCoord is the entry point for the mr-coord-vp proc. Args are
// [job, jobRoot, mapperbin, reducerbin, linesz, wordsz, nreduce, memPerTask,
// expectedMapDurMs, expectedReduceDurMs, slowTaskId, slowdownMs, input,
// binsz] -- job first because procgroupmgr prepends it (see
// StartValueProcsJob), exactly like NewCoord's own arg order.
func RunValueProcsCoord(args []string) {
	if len(args) != 14 {
		db.DFatalf("RunValueProcsCoord: wrong number of arguments %v", args)
	}
	jobName, jobRoot := args[0], args[1]
	mapperbin, reducerbin := args[2], args[3]
	linesz, wordsz := args[4], args[5]
	nreduce, err := strconv.Atoi(args[6])
	if err != nil {
		db.DFatalf("RunValueProcsCoord: nreduce %v isn't int", args[6])
	}
	mem, err := strconv.Atoi(args[7])
	if err != nil {
		db.DFatalf("RunValueProcsCoord: mem %v isn't int", args[7])
	}
	expectedMapDurMs, err := strconv.Atoi(args[8])
	if err != nil {
		db.DFatalf("RunValueProcsCoord: expectedMapDurMs %v isn't int", args[8])
	}
	expectedReduceDurMs, err := strconv.Atoi(args[9])
	if err != nil {
		db.DFatalf("RunValueProcsCoord: expectedReduceDurMs %v isn't int", args[9])
	}
	slowTaskId, err := strconv.ParseInt(args[10], 10, 64)
	if err != nil {
		db.DFatalf("RunValueProcsCoord: slowTaskId %v isn't int64", args[10])
	}
	slowdownMs, err := strconv.Atoi(args[11])
	if err != nil {
		db.DFatalf("RunValueProcsCoord: slowdownMs %v isn't int", args[11])
	}
	input := args[12]
	binsz, err := strconv.Atoi(args[13])
	if err != nil {
		db.DFatalf("RunValueProcsCoord: binsz %v isn't int", args[13])
	}
	mem64 := proc.Tmem(mem)

	sc, err := sigmaclnt.NewSigmaClnt(proc.GetProcEnv())
	if err != nil {
		db.DFatalf("RunValueProcsCoord: NewSigmaClnt err %v", err)
	}
	if err := sc.Started(); err != nil {
		db.DFatalf("RunValueProcsCoord: Started err %v", err)
	}

	b, err := sc.GetFile(JobOutLink(jobRoot, jobName))
	if err != nil {
		db.DFatalf("RunValueProcsCoord: GetFile JobOutLink err %v", err)
	}
	outdir := string(b)
	b, err = sc.GetFile(JobIntOutLink(jobRoot, jobName))
	if err != nil {
		db.DFatalf("RunValueProcsCoord: GetFile JobIntOutLink err %v", err)
	}
	intOutdir := string(b)

	bins, err := NewBins(sc.FsLib, input, true, sp.Tlength(binsz), sp.Tlength(SPLITSZ))
	if err != nil {
		db.DFatalf("RunValueProcsCoord: NewBins err %v", err)
	}
	nmap := len(bins)
	if nmap == 0 {
		db.DFatalf("RunValueProcsCoord: no input splits found under %v", input)
	}
	db.DPrintf(db.ALWAYS, "mr-coord-vp: job %v nmap %v nreduce %v", jobName, nmap, nreduce)

	c := clnt.NewClnt(sc.FsLib)

	// --- map phase: Select(nmap, Select(1, primary, dup)_0..nmap-1) ---
	mapLeaves := make([]*clnt.WorkNode, nmap)
	for i, bin := range bins {
		binJSON, err := json.Marshal(bin)
		if err != nil {
			db.DFatalf("RunValueProcsCoord: marshal bin %v err %v", bin, err)
		}
		// Only the primary attempt at the designated straggler task is
		// delayed -- racing a duplicate that's delayed too would mean nothing.
		slowdown := SlowdownOff
		if int64(i) == slowTaskId {
			slowdown = slowdownMs
		}
		primary := mapperLeaf(mapperbin, jobRoot, jobName, nreduce, string(binJSON), intOutdir, linesz, wordsz, slowdown, expectedMapDurMs, mem64, i)
		dup := mapperLeaf(mapperbin, jobRoot, jobName, nreduce, string(binJSON), intOutdir, linesz, wordsz, SlowdownOff, expectedMapDurMs, mem64, i)
		pair, err := taskPair(primary, dup)
		if err != nil {
			db.DFatalf("RunValueProcsCoord: map task %d: %v", i, err)
		}
		mapLeaves[i] = pair
	}
	mapRoot, err := clnt.Select(nmap, mapLeaves...)
	if err != nil {
		db.DFatalf("RunValueProcsCoord: map tree: %v", err)
	}

	mapTid := ValueProcsMapTid(jobName)
	if _, err := c.Submit(mapTid, "mr-map", mapRoot); err != nil {
		db.DFatalf("RunValueProcsCoord: Submit map tree: %v", err)
	}
	mapResults, mapState, err := c.Wait(mapTid)
	if err != nil {
		db.DFatalf("RunValueProcsCoord: Wait map tree: %v", err)
	}
	if len(mapResults) != nmap {
		db.DFatalf("RunValueProcsCoord: map phase settled as %v with %d/%d tasks done", mapState, len(mapResults), nmap)
	}

	obins := make([]Bin, nmap)
	for _, res := range mapResults {
		idx, err := labelIndex(res.Label, "m")
		if err != nil {
			db.DFatalf("RunValueProcsCoord: %v", err)
		}
		r, err := NewResult(res.Status.Data())
		if err != nil {
			db.DFatalf("RunValueProcsCoord: decode map result %v: %v", res.Label, err)
		}
		obins[idx] = r.OutBin
		// Appended for mr.PrintMRStats, exactly as processResult does for the
		// baseline coordinator. MsOuter stays whatever the worker itself
		// reported (0 -- see mr.Result's doc) since this coordinator has no
		// per-attempt wall-clock measurement of its own to overwrite it with,
		// unlike processResult's res.Ms; only affects the "slot pressure"
		// diagnostic PrintMRStats prints, not this test's assertions.
		if err := sc.AppendFileJson(MRstats(jobRoot, jobName), r); err != nil {
			db.DFatalf("RunValueProcsCoord: AppendFileJson %v err %v", MRstats(jobRoot, jobName), err)
		}
	}

	// Transpose mapper outputs into one input Bin per reducer, exactly as
	// makeReduceBins does for the baseline coordinator.
	reduceBinIn := make([]Bin, nreduce)
	for r := 0; r < nreduce; r++ {
		reduceBinIn[r] = make(Bin, nmap)
	}
	for j, obin := range obins {
		for i, s := range obin {
			reduceBinIn[i][j] = s
		}
	}

	// --- reduce phase: Select(nreduce, Select(1, primary, dup)_0..nreduce-1) ---
	reduceLeaves := make([]*clnt.WorkNode, nreduce)
	for r := 0; r < nreduce; r++ {
		outlink := ReduceOut(jobRoot, jobName) + strconv.Itoa(r)
		outTarget := ReduceOutTarget(outdir, jobName) + strconv.Itoa(r)
		binJSON, err := json.Marshal(reduceBinIn[r])
		if err != nil {
			db.DFatalf("RunValueProcsCoord: marshal reduce bin %v err %v", reduceBinIn[r], err)
		}
		primary := reducerLeaf(reducerbin, r, outlink, outTarget, nmap, string(binJSON), expectedReduceDurMs, mem64)
		dup := reducerLeaf(reducerbin, r, outlink, outTarget, nmap, string(binJSON), expectedReduceDurMs, mem64)
		pair, err := taskPair(primary, dup)
		if err != nil {
			db.DFatalf("RunValueProcsCoord: reduce task %d: %v", r, err)
		}
		reduceLeaves[r] = pair
	}
	reduceRoot, err := clnt.Select(nreduce, reduceLeaves...)
	if err != nil {
		db.DFatalf("RunValueProcsCoord: reduce tree: %v", err)
	}

	reduceTid := ValueProcsReduceTid(jobName)
	if _, err := c.Submit(reduceTid, "mr-reduce", reduceRoot); err != nil {
		db.DFatalf("RunValueProcsCoord: Submit reduce tree: %v", err)
	}
	reduceResults, reduceState, err := c.Wait(reduceTid)
	if err != nil {
		db.DFatalf("RunValueProcsCoord: Wait reduce tree: %v", err)
	}
	if len(reduceResults) != nreduce {
		db.DFatalf("RunValueProcsCoord: reduce phase settled as %v with %d/%d tasks done", reduceState, len(reduceResults), nreduce)
	}
	for _, res := range reduceResults {
		r, err := NewResult(res.Status.Data())
		if err != nil {
			db.DFatalf("RunValueProcsCoord: decode reduce result %v: %v", res.Label, err)
		}
		if err := sc.AppendFileJson(MRstats(jobRoot, jobName), r); err != nil {
			db.DFatalf("RunValueProcsCoord: AppendFileJson %v err %v", MRstats(jobRoot, jobName), err)
		}
	}

	db.DPrintf(db.ALWAYS, "mr-coord-vp: job %v done", jobName)
	JobDone(sc.FsLib, jobRoot, jobName)
	sc.ClntExit(proc.NewStatus(proc.StatusOK))
}
