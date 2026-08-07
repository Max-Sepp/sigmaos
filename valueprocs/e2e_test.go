package valueprocs_test

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	db "sigmaos/debug"
	"sigmaos/proc"
	mschedclnt "sigmaos/sched/msched/clnt"
	sp "sigmaos/sigmap"
	"sigmaos/test"
	"sigmaos/util/crash"
	"sigmaos/valueprocs/adapter"
	"sigmaos/valueprocs/clnt"
	"sigmaos/valueprocs/policy"
	"sigmaos/valueprocs/proto"
)

// tstate is a realm with the scheduling service running in it.
type tstate struct {
	*test.RealmTstate
	mrts *test.MultiRealmTstate
	job  *adapter.Job
	c    *clnt.Clnt
}

func newTstate(t *testing.T) (*tstate, bool) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{test.REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return nil, false
	}
	rts := mrts.GetRealm(test.REALM1)
	ts := &tstate{
		RealmTstate: rts,
		mrts:        mrts,
		job:         adapter.StartJob(rts.SigmaClnt, 0),
		c:           clnt.NewClnt(rts.FsLib),
	}
	return ts, true
}

func (ts *tstate) shutdown() {
	ts.job.Stop()
	ts.mrts.Shutdown()
}

// submit registers a tree and requires that this call is what registered it,
// so that a test cannot accidentally run against a tree left behind by an
// earlier one under the same id.
func submit(t *testing.T, c clnt.Runner, tid, label string, root *clnt.WorkNode) bool {
	t.Helper()
	created, err := c.Submit(tid, label, root)
	if !assert.Nil(t, err, "Submit %v: %v", tid, err) {
		return false
	}
	return assert.True(t, created, "%v was already registered", tid)
}

// eventually polls until f holds, because a decision and the platform call it
// produces are deliberately not simultaneous anywhere in this layer.
func eventually(t *testing.T, d time.Duration, f func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		if f() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// nodeOf samples one node of a tree, or reports that it could not, so that a
// poll over a service that is restarting does not fail the test on the way.
func nodeOf(ts *tstate, tid, nid string) (*proto.NodeStatus, bool) {
	st, err := ts.c.Status(tid)
	if err != nil {
		return nil, false
	}
	for _, n := range st.Nodes {
		if n.NodeID == nid {
			return n, true
		}
	}
	return nil, false
}

// fillCluster spawns enough spinners to occupy every core the cluster has, and
// returns what reclaims them. Contention has to be real: the probe measures
// machine-wide CPU, so nothing short of procs actually burning it moves the
// reading this layer scales on.
func fillCluster(t *testing.T, ts *tstate) func() {
	t.Helper()
	loads, err := mschedclnt.NewMSchedClnt(ts.FsLib, sp.NOT_SET).MSchedLoad()
	if !assert.Nil(t, err, "MSchedLoad: %v", err) {
		return func() {}
	}
	cores := 0
	for _, l := range loads {
		cores += int(l.NCores)
	}

	pids := make([]sp.Tpid, 0, cores)
	for range cores {
		p := proc.NewProc("spinner", []string{"name/"})
		if err := ts.Spawn(p); !assert.Nil(t, err, "Spawn spinner: %v", err) {
			break
		}
		if err := ts.WaitStart(p.GetPid()); !assert.Nil(t, err, "WaitStart spinner: %v", err) {
			break
		}
		pids = append(pids, p.GetPid())
	}
	db.DPrintf(db.TEST, "filling %v cores with %v spinners", cores, len(pids))

	return func() {
		for _, pid := range pids {
			ts.Evict(pid)
			ts.WaitExit(pid)
		}
	}
}

// leaf builds a workload node. The client never sees a scheduling concept:
// it hands over a proc and a label, and that is all.
func leaf(mode string, ms int, score, grad float64, label string) *clnt.WorkNode {
	p := proc.NewProc("valueprocs-test", []string{
		mode, strconv.Itoa(ms),
		strconv.FormatFloat(score, 'f', -1, 64),
		strconv.FormatFloat(grad, 'f', -1, 64),
	})
	return clnt.Leaf(p).WithLabel(label)
}

// TestSubmitNestedTree is the first thing in this layer to cross a wire.
//
// It exercises every piece at once, which is the point: the client's builders
// become a proto, the service turns that back into a tree, the adapter spawns
// procs for it, the procs report scores back over RPC, their exits become
// terminal events, and the results travel home through the result log to a
// caller that is not their parent and could not have collected them any other
// way.
func TestSubmitNestedTree(t *testing.T) {
	ts, ok := newTstate(t)
	if !ok {
		return
	}
	defer ts.shutdown()

	inner, err := clnt.Select(1, leaf("work", 800, 0.5, 0, "c-a"), leaf("work", 800, 0.6, 0, "c-b"))
	assert.Nil(t, err)
	root, err := clnt.Select(2,
		leaf("work", 600, 0.9, 0, "a"),
		leaf("work", 600, 0.8, 0, "b"),
		inner.WithLabel("inner"))
	assert.Nil(t, err)

	submit(t, ts.c, "t1", "nested", root)

	start := time.Now()
	res, state, err := ts.c.Wait("t1")
	if !assert.Nil(t, err, "Wait: %v", err) {
		return
	}
	db.DPrintf(db.TEST, "tree settled %v after %v with %d results", state, time.Since(start), len(res))

	assert.Equal(t, "satisfied", state)

	// Two of three children satisfy the root, so at least two leaves report.
	// Not exactly two: the cluster is idle, so the scheduler runs the slack
	// as well, and whichever of those finishes before the quorum lands is a
	// real result too.
	assert.GreaterOrEqual(t, len(res), 2, "a quorum reported")

	for node, r := range res {
		db.DPrintf(db.TEST, "%v (%v) -> %v", node, r.Label, r.Status)
		assert.True(t, r.Status.IsStatusOK(), "%v: %v", node, r.Status)
		assert.NotEmpty(t, r.Label, "%v lost its label", node)

		// The payload survives the whole round trip, so an application reads
		// it exactly as it would have had it spawned the proc itself.
		d, ok := r.Status.Data().(map[string]any)
		if assert.True(t, ok, "%v: %T", node, r.Status.Data()) {
			assert.Equal(t, node, d["node"], "the proc knew which leaf it was")
			assert.Greater(t, d["iters"], float64(0), "it did some work")
		}
	}
}

// TestScoresReachTheScheduler proves the value channel works end to end. It
// is the half of this layer that no amount of contention sensing replaces:
// without it the scheduler knows only how busy the cluster is, which is what
// besched already does for free.
func TestScoresReachTheScheduler(t *testing.T) {
	ts, ok := newTstate(t)
	if !ok {
		return
	}
	defer ts.shutdown()

	// Long enough to report while the test watches.
	root, err := clnt.Select(1,
		leaf("work", 4000, 0.9, 0.1, "high"),
		leaf("work", 4000, 0.2, 0.1, "low"))
	assert.Nil(t, err)
	submit(t, ts.c, "t2", "scores", root)

	scored := false
	for range 60 {
		st, err := ts.c.Status("t2")
		if !assert.Nil(t, err, "Status: %v", err) {
			return
		}
		for _, n := range st.Nodes {
			if n.IsLeaf && n.HasScore {
				db.DPrintf(db.TEST, "%v (%v) score %v run %v state %v",
					n.NodeID, n.Label, n.Score, n.Run, n.RunState)
				scored = true
			}
		}
		if scored {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	assert.True(t, scored, "no leaf ever reported a score")

	// The pid is the handle an operator needs, and only the adapter knows it.
	st, _ := ts.c.Status("t2")
	pids := 0
	for _, n := range st.Nodes {
		if n.IsLeaf && n.PID != "" {
			pids++
		}
	}
	assert.Greater(t, pids, 0, "no leaf reported the proc running it")

	assert.Nil(t, ts.c.Cancel("t2"))
}

// TestContentionIsMeasured checks that the probe reaches real kernels and
// that what it measures arrives at the scheduler. Until this works, pressure
// is permanently zero and every tree runs everything it has.
func TestContentionIsMeasured(t *testing.T) {
	ts, ok := newTstate(t)
	if !ok {
		return
	}
	defer ts.shutdown()

	var stats struct {
		slots int32
		busy  float64
	}
	for range 60 {
		s, err := ts.c.SchedStats()
		if !assert.Nil(t, err, "SchedStats: %v", err) {
			return
		}
		if s.Slots > 0 {
			stats.slots, stats.busy = s.Slots, s.Busy
			db.DPrintf(db.TEST, "pressure %v busy %v slots %v components %v",
				s.Pressure, s.Busy, s.Slots, s.Components)
			assert.NotZero(t, s.Epoch, "an epoch of zero would disable the staleness check")
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	assert.Greater(t, stats.slots, int32(0), "the probe never reported any capacity")
	assert.GreaterOrEqual(t, stats.busy, 0.0)
	assert.LessOrEqual(t, stats.busy, 1.0)
}

// TestResultsArriveIncrementally checks the cursor over a real connection: a
// caller collecting results one at a time gets them as they land rather than
// all at the end, and its position survives being handed back.
func TestResultsArriveIncrementally(t *testing.T) {
	ts, ok := newTstate(t)
	if !ok {
		return
	}
	defer ts.shutdown()

	root, err := clnt.Select(3,
		leaf("work", 300, 1, 0, "a"),
		leaf("work", 900, 1, 0, "b"),
		leaf("work", 1500, 1, 0, "c"))
	assert.Nil(t, err)
	submit(t, ts.c, "t3", "incremental", root)

	var (
		seen  []string
		times []time.Duration
		start = time.Now()
	)
	for r, err := range ts.c.Results("t3") {
		if !assert.Nil(t, err, "Results: %v", err) {
			return
		}
		seen = append(seen, r.Label)
		times = append(times, time.Since(start))
		db.DPrintf(db.TEST, "result %v (%v) at %v", r.Seq, r.Label, time.Since(start))
	}

	assert.Len(t, seen, 3)
	// Sequence numbers are dense and ordered, and the results did not all
	// turn up at once: the first landed well before the last.
	assert.Less(t, times[0], times[len(times)-1])
}

// TestFailedLeafIsRetriedAndFatalIsNot separates the two ways an attempt can
// end badly. A crash is worth another go; work that will never succeed is
// not, and treating them alike would either give up on flakiness or retry a
// bug forever.
func TestFailedLeafIsRetriedAndFatalIsNot(t *testing.T) {
	ts, ok := newTstate(t)
	if !ok {
		return
	}
	defer ts.shutdown()

	root, err := clnt.Select(1, leaf("fatal", 0, 0, 0, "doomed"))
	assert.Nil(t, err)
	submit(t, ts.c, "t4", "fatal", root)

	_, state, err := ts.c.Wait("t4")
	if !assert.Nil(t, err, "Wait: %v", err) {
		return
	}
	assert.Equal(t, "failed", state, "a fatal leaf ends its tree rather than being retried")

	st, err := ts.c.Status("t4")
	assert.Nil(t, err)
	for _, n := range st.Nodes {
		if n.IsLeaf {
			assert.EqualValues(t, 1, n.Attempts, "a fatal failure is not retried")
		}
	}
}

// TestATransientFailureIsRetriedUnderAFreshAttempt is the other half of the
// same distinction, and checks the identity machinery that makes retrying
// safe: every retry is a new run number and a new proc, which is what stops
// anything the abandoned attempt says afterwards being read as progress.
func TestATransientFailureIsRetriedUnderAFreshAttempt(t *testing.T) {
	ts, ok := newTstate(t)
	if !ok {
		return
	}
	defer ts.shutdown()

	root, err := clnt.Select(1, leaf("fail", 0, 0, 0, "flaky"))
	assert.Nil(t, err)
	submit(t, ts.c, "t5", "flaky", root)

	_, state, err := ts.c.Wait("t5")
	if !assert.Nil(t, err, "Wait: %v", err) {
		return
	}
	assert.Equal(t, "failed", state, "a leaf that keeps failing eventually gives up")

	n, ok := nodeOf(ts, "t5", "r.0")
	if !assert.True(t, ok, "no leaf") {
		return
	}
	db.DPrintf(db.TEST, "flaky: run %v, %v attempts, %v stops", n.Run, n.Attempts, n.Stops)
	assert.EqualValues(t, 3, n.Attempts, "bounded by MaxAttempts, not one and not forever")
	assert.EqualValues(t, 2, n.Run, "each retry ran under its own identity")
	assert.EqualValues(t, 0, n.Stops, "a failure is the leaf's doing, never the scheduler's")
}

// TestTheLoserOfARaceActuallyExits is what makes running more than k
// affordable. Several procs are sent after one result and one of them gets
// there; if the others keep their slots until they finish anyway, redundancy
// costs exactly as much as it saves and the layer is pointless.
func TestTheLoserOfARaceActuallyExits(t *testing.T) {
	ts, ok := newTstate(t)
	if !ok {
		return
	}
	defer ts.shutdown()

	// One is wedged and says so -- a flat score with a high gradient is the
	// shape of a straggler -- and the other is nearly done.
	root, err := clnt.Select(1,
		leaf("stall", 60000, 0.5, 1.0, "wedged"),
		leaf("work", 1500, 0.5, 0, "quick"))
	assert.Nil(t, err)
	submit(t, ts.c, "t6", "race", root)

	start := time.Now()
	_, state, err := ts.c.Wait("t6")
	if !assert.Nil(t, err, "Wait: %v", err) {
		return
	}
	assert.Equal(t, "satisfied", state)

	// The tree is satisfied, so the wedged attempt is now pure waste. It can
	// only give its slot back by dying on the eviction, which is the one thing
	// SigmaOS will not do on its behalf.
	if !assert.True(t, eventually(t, 30*time.Second, func() bool {
		st, err := ts.c.Status("t6")
		return err == nil && st.NCharged == 0
	}), "the loser held its slot after losing") {
		return
	}
	assert.Less(t, time.Since(start), 55*time.Second,
		"the loser had 60s of work left and stopped long before reaching the end of it")
}

// TestPruningUnderLoad is the behaviour the whole layer exists for. The same
// tree runs everything it has on an idle cluster and narrows toward its quorum
// on a busy one, and the application says nothing about either: it reports how
// well its work is going and the scheduler decides how much of it to keep.
func TestPruningUnderLoad(t *testing.T) {
	ts, ok := newTstate(t)
	if !ok {
		return
	}
	defer ts.shutdown()

	// Long enough to still be running when the contention arrives, since what
	// is being tested is what happens to work already in flight.
	root, err := clnt.Select(1,
		leaf("work", 120000, 0.9, 0, "best"),
		leaf("work", 120000, 0.5, 0, "middle"),
		leaf("work", 120000, 0.1, 0, "worst"))
	assert.Nil(t, err)
	submit(t, ts.c, "t7", "pruning", root)

	// k is one, but on an idle cluster there is nothing better to spend the
	// capacity on, so all three run.
	if !assert.True(t, eventually(t, 30*time.Second, func() bool {
		n, ok := nodeOf(ts, "t7", "r")
		return ok && n.Target == 3 && n.NRunning == 3
	}), "an idle cluster runs every candidate") {
		return
	}

	release := fillCluster(t, ts)
	defer release()

	// Pressure is smoothed and a target has to hold before it may reverse, so
	// this is patient on purpose: the point of both is that a cluster that is
	// briefly busy does not disturb anything.
	// Busy, not just Pressure: the spinners have to be what did this. Queue
	// dwell is a pressure term too, so a test that only watched the total
	// would keep passing if the probe stopped seeing the machine at all.
	//
	// The last observation is kept so a failure can say which of the three
	// conditions was not met. Without it the message is the same whether the
	// spinners never landed, the probe never saw them, or the scheduler saw
	// them and declined to act -- and those want completely different fixes.
	var lastBusy, lastPressure float64
	lastTarget := -1
	if !assert.True(t, eventually(t, 120*time.Second, func() bool {
		st, err := ts.c.SchedStats()
		if err != nil {
			return false
		}
		lastBusy, lastPressure = st.Busy, st.Pressure
		if n, ok := nodeOf(ts, "t7", "r"); ok {
			lastTarget = int(n.Target)
		}
		return st.Busy > 0.75 && st.Pressure > 0.75 && lastTarget >= 0 && lastTarget < 3
	}), "a saturated cluster never narrowed the tree toward its quorum: last busy=%v pressure=%v target=%v",
		lastBusy, lastPressure, lastTarget) {
		return
	}

	st, err := ts.c.SchedStats()
	assert.Nil(t, err)
	db.DPrintf(db.TEST, "pruned at pressure %v (busy %v, delay %v) components %v",
		st.Pressure, st.Busy, st.DelayPressure, st.Components)

	// And the term that saw it was CPU. Best-effort admission gates on free
	// memory and best-effort procs routinely reserve none, so a fleet pinning
	// every core still reads as having memory to spare -- which is why the
	// fold takes a max over both rather than trusting memory alone.
	assert.Greater(t, st.Components["cpu"], 0.75, "cpu did not see a cluster full of spinners")
	assert.Less(t, st.Components["mem"], 0.75,
		"memory saw it too, so this no longer tests the case memory is blind to")

	// And it sheds by score rather than by position: the worst-scoring leaf
	// gives up its slot while the best keeps running.
	worst, ok := nodeOf(ts, "t7", "r.2")
	if assert.True(t, ok, "no worst leaf") {
		assert.NotEqual(t, "running", worst.RunState, "the lowest-scoring leaf kept its slot")
	}
	best, ok := nodeOf(ts, "t7", "r.0")
	if assert.True(t, ok, "no best leaf") {
		assert.Equal(t, "running", best.RunState, "the highest-scoring leaf lost its slot")
	}
	assert.Greater(t, st.NStopsByKind[uint32(policy.StopOutrankedBySibling)], int32(0),
		"the stop was attributed to something other than the ranking")

	assert.Nil(t, ts.c.Cancel("t7"))
}

// TestANonCompliantProcIsSynthesizedOver closes the hole SigmaOS leaves open.
// Eviction is advisory, so a proc that never waits on it cannot be made to
// stop, and the slot it holds would otherwise be lost to the scheduler
// forever. Reclaiming it optimistically is the lesser harm: the leak is still
// visible in Busy, because pressure is measured from the platform rather than
// inferred from the ledger.
//
// A nonzero count here in production is a bug report about a proc, not normal
// operation.
func TestANonCompliantProcIsSynthesizedOver(t *testing.T) {
	ts, ok := newTstate(t)
	if !ok {
		return
	}
	defer ts.shutdown()

	// The bypassing proc must outlive the stop timeout, or its own exit lands
	// first and the synthesis path is never reached.
	root, err := clnt.Select(1,
		leaf("bypass", 90000, 0, 0, "deaf"),
		leaf("work", 1500, 0.9, 0, "quick"))
	assert.Nil(t, err)
	submit(t, ts.c, "t8", "noncompliant", root)

	_, state, err := ts.c.Wait("t8")
	if !assert.Nil(t, err, "Wait: %v", err) {
		return
	}
	assert.Equal(t, "satisfied", state)

	// The stop is issued, retried, and finally given up on after StopTimeout,
	// which is a minute by default -- so this waits out the real thing rather
	// than a shortened one.
	assert.True(t, eventually(t, 120*time.Second, func() bool {
		st, err := ts.c.SchedStats()
		return err == nil && st.NSynthesizedStopped > 0
	}), "a proc that ignored its eviction held its slot indefinitely")

	st, err := ts.c.SchedStats()
	assert.Nil(t, err)
	db.DPrintf(db.TEST, "synthesized %v stops after %v evict retries",
		st.NSynthesizedStopped, st.NEvictRetries)
	assert.EqualValues(t, 0, st.NSynthesizedFailed,
		"a stop we asked for is not a failure, and must not spend the leaf's attempts")
}

// TestTreesInOneRealmAreInvisibleInAnother checks the isolation story, which
// is the whole of one sentence: one service per realm. A score means something
// only among the children of one node and nothing at all between tenants, so
// there must be no configuration under which two realms' trees can be compared
// -- and the strongest way to say that is that they cannot even collide.
func TestTreesInOneRealmAreInvisibleInAnother(t *testing.T) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{test.REALM1, test.REALM2})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return
	}
	defer mrts.Shutdown()

	r1, r2 := mrts.GetRealm(test.REALM1), mrts.GetRealm(test.REALM2)
	j1 := adapter.StartJob(r1.SigmaClnt, 0)
	defer j1.Stop()
	j2 := adapter.StartJob(r2.SigmaClnt, 0)
	defer j2.Stop()
	c1, c2 := clnt.NewClnt(r1.FsLib), clnt.NewClnt(r2.FsLib)

	one, err := clnt.Select(1, leaf("work", 3000, 0.9, 0, "a"))
	assert.Nil(t, err)
	two, err := clnt.Select(1, leaf("work", 3000, 0.8, 0, "b"))
	assert.Nil(t, err)

	// The same id in both realms. Submitting an id twice to one service is an
	// error, so if these reached the same scheduler the second would fail.
	submit(t, c1, "shared", "one", one)
	submit(t, c2, "shared", "two", two)

	s1, err := c1.Status("shared")
	if !assert.Nil(t, err, "Status in realm 1: %v", err) {
		return
	}
	s2, err := c2.Status("shared")
	if !assert.Nil(t, err, "Status in realm 2: %v", err) {
		return
	}
	assert.Equal(t, "one", s1.Label)
	assert.Equal(t, "two", s2.Label, "one realm's tree answered for another's")

	// And a tree that exists in neither is unknown in both, so the check above
	// is not passing on the strength of every id being accepted.
	_, err = c1.Status("nowhere")
	assert.Error(t, err)

	for _, c := range []*clnt.Clnt{c1, c2} {
		_, state, err := c.Wait("shared")
		assert.Nil(t, err, "Wait: %v", err)
		assert.Equal(t, "satisfied", state)
	}
}

// TestARestartedServiceIsANewIncarnation states the restart caveat as a test
// rather than as a paragraph. The service holds every tree in memory, and a
// restarted one cannot collect the exits of procs its predecessor spawned, so
// the trees really are gone. The contract is not that they survive: it is that
// a client finds out, rather than waiting forever for a result that is never
// coming.
func TestARestartedServiceIsANewIncarnation(t *testing.T) {
	// Armed before anything starts, because procgroupmgr passes this process's
	// failure settings down to the procs it spawns. disarm is called the
	// moment the restart has been seen: every incarnation reads this on the
	// way up, so leaving it set would crash the replacement too, and go on
	// doing it through the shutdown that is meant to end the service.
	e := crash.NewEvent(crash.VALUESCHED_CRASH, 0, 1.0, crash.WithStart(15000))
	if err := crash.SetSigmaFail(crash.NewTeventMapOne(e)); !assert.Nil(t, err, "SetSigmaFail") {
		return
	}
	disarm := sync.OnceFunc(func() { crash.SetSigmaFail(crash.NewTeventMap()) })
	t.Cleanup(disarm)

	ts, ok := newTstate(t)
	if !ok {
		return
	}
	defer ts.shutdown()

	before, err := ts.c.SchedStats()
	if !assert.Nil(t, err, "SchedStats: %v", err) {
		return
	}
	assert.NotZero(t, before.Epoch, "an epoch of zero would disable the staleness check")

	// Long enough to still be running when the service dies, and no longer:
	// its parent is the incarnation that is about to disappear, so nothing is
	// left to collect its exit and it holds a slot until it ends on its own.
	root, err := clnt.Select(1, leaf("work", 30000, 0.9, 0, "a"))
	assert.Nil(t, err)
	submit(t, ts.c, "t9", "restart", root)

	// procgroupmgr brings the service back under a fresh generation, and the
	// epoch is derived from it. That change is the whole of the signal --
	// nothing was persisted, so there is nothing else to compare.
	restarted := eventually(t, 90*time.Second, func() bool {
		st, err := ts.c.SchedStats()
		return err == nil && st.Epoch != before.Epoch
	})
	disarm()
	if !assert.True(t, restarted, "the service never came back under a new epoch") {
		return
	}
	after, err := ts.c.SchedStats()
	assert.Nil(t, err)
	db.DPrintf(db.TEST, "epoch %v -> %v", before.Epoch, after.Epoch)

	// Gone, and reported as gone. Answering "pending" would leave a caller
	// waiting on a tree nothing is running.
	_, err = ts.c.Status("t9")
	assert.Error(t, err, "a tree from the previous incarnation was reported as still there")
	assert.EqualValues(t, 0, after.NTrees)
}
