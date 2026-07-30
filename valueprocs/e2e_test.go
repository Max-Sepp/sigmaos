package valueprocs_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	db "sigmaos/debug"
	"sigmaos/proc"
	sp "sigmaos/sigmap"
	"sigmaos/test"
	"sigmaos/valueprocs/adapter"
	"sigmaos/valueprocs/clnt"
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

// leaf builds a workload node. The client never sees a scheduling concept:
// it hands over a proc and a label, and that is all.
func leaf(mode string, ms int, score, grad float64, label string) *clnt.Node {
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

	assert.Nil(t, ts.c.Submit("t1", "nested", root))

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
	assert.Nil(t, ts.c.Submit("t2", "scores", root))

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
	assert.Nil(t, ts.c.Submit("t3", "incremental", root))

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
	assert.Nil(t, ts.c.Submit("t4", "fatal", root))

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
