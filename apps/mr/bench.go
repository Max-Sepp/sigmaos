package mr

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/dustin/go-humanize"

	"sigmaos/sigmaclnt/fslib"
	sp "sigmaos/sigmap"
	"sigmaos/test"
)

func PrintMRStats(fsl *fslib.FsLib, jobRoot, job string) error {
	rdr, err := fsl.OpenReader(MRstats(jobRoot, job))
	if err != nil {
		return err
	}
	dec := json.NewDecoder(rdr)
	fmt.Println("==== STATS:")
	totIn := sp.Tlength(0)
	totOut := sp.Tlength(0)
	totWTmp := sp.Tlength(0)
	totRTmp := sp.Tlength(0)
	all := []*Result{}
	winners := []*Result{}
	for {
		r := &Result{}
		if err := dec.Decode(r); err == io.EOF {
			break
		}
		all = append(all, r)
		// Lost entries are a discarded speculative sibling of a task the
		// baseline coordinator already recorded a winner for (see
		// apps/mr/coord.go's processResult) -- keep them out of the
		// per-task/aggregate numbers below so backup/original races don't
		// double-count a task's real input/output, and report them
		// separately instead.
		if r.Lost {
			continue
		}
		winners = append(winners, r)
		if r.IsM {
			totIn += r.In
			totWTmp += r.Out
		} else {
			totOut += r.Out
			totRTmp += r.In
		}
	}
	sort.Slice(winners, func(i, j int) bool {
		return test.Tput(winners[i].In+winners[i].Out, winners[i].MsInner) > test.Tput(winners[j].In+winners[j].Out, winners[j].MsInner)
	})

	// over is a task's overhead, MsOuter-MsInner, the wall time it existed
	// but wasn't doing real work, i.e. time spent waiting for an admission slot.
	// map/reduce slot-wait: Tot = summed, Max = worst.
	var mOverTot, rOverTot, mOverMax, rOverMax int64
	var nM, nR int
	for _, r := range winners {
		over := max(r.MsOuter-r.MsInner, 0)
		if r.IsM {
			mOverTot += over
			nM++
			mOverMax = max(mOverMax, over)
		} else {
			rOverTot += over
			nR++
			rOverMax = max(rOverMax, over)
		}
		backupTag := ""
		if r.IsBackup {
			backupTag = " [WON AS BACKUP]"
		}
		fmt.Printf("[%s, kid:%v, taskId:%d]%s:\n\tin %v out %v tot %v inner %vms outer %vms (%s)\n", r.Task, r.KernelID, r.TaskId, backupTag, humanize.Bytes(uint64(r.In)), humanize.Bytes(uint64(r.Out)), test.Mbyte(r.In+r.Out), r.MsInner, r.MsOuter, test.TputStr(r.In+r.Out, r.MsInner))
	}
	fmt.Printf("==== totIn %s (%d) totOut %s tmpOut %s tmpIn %s\n",
		humanize.Bytes(uint64(totIn)), totIn,
		humanize.Bytes(uint64(totOut)),
		humanize.Bytes(uint64(totWTmp)),
		humanize.Bytes(uint64(totRTmp)),
	)

	mOverMean, rOverMean := int64(0), int64(0)
	if nM > 0 {
		mOverMean = mOverTot / int64(nM)
	}
	if nR > 0 {
		rOverMean = rOverTot / int64(nR)
	}
	fmt.Printf("==== slot pressure (outer-inner queueing overhead): map total %dms mean %dms max %dms (n=%d); reduce total %dms mean %dms max %dms (n=%d)\n",
		mOverTot, mOverMean, mOverMax, nM, rOverTot, rOverMean, rOverMax, nR)

	printSpeculativeDecisions(all)
	return nil
}

// printSpeculativeDecisions logs every speculative-execution decision the
// baseline coordinator made (apps/mr/coord.go's speculate/processResult):
// each backup that fired, and how it and its sibling were resolved (which one
// won, which lost and how much wall-time it wasted). Lets a benchmark's own
// log retrace speculation task-by-task instead of only via the aggregate
// Nspeculate/Nwasted/MsWasted counters.
func printSpeculativeDecisions(all []*Result) {
	decisions := make([]*Result, 0)
	for _, r := range all {
		if r.IsBackup || r.Lost {
			decisions = append(decisions, r)
		}
	}
	if len(decisions) == 0 {
		fmt.Println("==== speculative execution: no backups fired")
		return
	}
	fmt.Println("==== speculative execution decisions:")
	for _, r := range decisions {
		role := "backup"
		if !r.IsBackup {
			role = "original"
		}
		outcome := "won"
		if r.Lost {
			outcome = "lost"
		}
		phase := "reduce"
		if r.IsM {
			phase = "map"
		}
		fmt.Printf("  [%s %s attempt %s] taskId=%d proc=%s runMs=%d\n", phase, role, outcome, r.TaskId, r.Task, r.MsOuter)
	}
	fmt.Printf("==== %d speculative decisions recorded\n", len(decisions))
}

func RemoveJob(fsl *fslib.FsLib, jobRoot, job string) error {
	return fsl.RmDir(JobDir(jobRoot, job))
}
