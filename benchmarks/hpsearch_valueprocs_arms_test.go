package benchmarks_test

// The value-procs HPSearch arms that TestHPSearchValueProcs does not cover.
//
// Two groups. The first is the under-utilisation half of the claim that an
// application "does not know if it either under utilised or over utilised the
// available compute": every existing arm submits Select(1, 15) onto a host
// with far fewer slots than that, so the search is capacity-bound even when
// nothing else is running, and no arm anywhere asks whether it expands into
// slack it is given. The second is the negative controls -- what a trial that
// misreports, or does not report at all, costs the honest ones.
//
// All of them read the scheduler's own trace (see valueprocs_sampler_test.go)
// rather than inferring admission from makespan, because "how many ran at
// once" is the quantity these claims are actually about.

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"sigmaos/apps/hpsearch"
	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/sigmaclnt"
	sp "sigmaos/sigmap"
	"sigmaos/test"
	"sigmaos/valueprocs/adapter"
	"sigmaos/valueprocs/clnt"
)

const (
	// HPSearchInflateFactor is how much the dishonest trial multiplies its
	// reported score by. Large enough that it outranks every honest trial at
	// every iteration, which is the worst case rather than a marginal one.
	HPSearchInflateFactor = 10.0
)

// hpsearchVPFixture is the boilerplate every arm below repeats: a realm, a
// valuesched job, and a client for it.
type hpsearchVPFixture struct {
	mrts  *test.MultiRealmTstate
	sc    *sigmaclnt.SigmaClnt
	vpc   *clnt.Clnt
	vpjob *adapter.Job
	cfg   *hpsearch.Config
}

// newHPSearchVPFixture boots a realm and starts valuesched, or returns nil
// after failing the test.
func newHPSearchVPFixture(t *testing.T) *hpsearchVPFixture {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return nil
	}
	cfg := hpsearch.DefaultConfig()
	sc := mrts.GetRealm(REALM1).SigmaClnt
	f := &hpsearchVPFixture{mrts: mrts, sc: sc, cfg: cfg}
	f.vpjob = adapter.StartJob(sc, 0)
	f.vpc = clnt.NewClnt(sc.FsLib)
	return f
}

func (f *hpsearchVPFixture) shutdown() {
	f.vpjob.Stop()
	f.mrts.Shutdown()
}

// runVP runs one value-procs search under a sampler and returns the analysis
// of it alongside the scheduler trace.
func (f *hpsearchVPFixture) runVP(t *testing.T, label string) (*hpsearch.LivePruneResult, vpSummary, bool) {
	smp := startVPSampler(f.vpc)
	j, err := hpsearch.StartValueProcsJob(f.vpc, f.cfg)
	if !assert.Nil(t, err, "Error StartValueProcsJob: %v", err) {
		smp.summarize()
		return nil, vpSummary{}, false
	}
	outcomes, err := j.Wait()
	trace := smp.reportTrace(label)
	if !assert.Nil(t, err, "Error Wait: %v", err) {
		return nil, trace, false
	}
	live := hpsearch.AnalyzeValueProcs(outcomes, j.Curves(outcomes), f.cfg)
	best := hpsearch.Best(outcomes)
	if !assert.NotNil(t, best, "%v: no trial finished", label) {
		return nil, trace, false
	}
	db.DPrintf(db.ALWAYS, "%v selection: %d of %d trials finished, application picked config %d",
		label, hpsearch.NFinished(outcomes), f.cfg.NConfigs, best.ConfigId)
	if st, err := j.Status(); err == nil {
		db.DPrintf(db.ALWAYS, "%v final tree: %s", label, dumpTreeStatus(st))
	}
	return live, trace, true
}

// SchedSlotsWait is how long schedSlots waits for the first occupancy probe.
const SchedSlotsWait = 30 * time.Second

// schedSlots asks the scheduler how many slots it thinks the cluster has, so
// an arm can size itself relative to capacity rather than hardcoding a number
// that is only right on one host.
//
// It polls rather than asking once. A freshly started scheduler has not been
// told the cluster's size yet -- Slots arrives with the first occupancy probe,
// some hundreds of milliseconds later -- and until then it reports 0, which is
// indistinguishable from "no capacity" to a caller that only looks once. Every
// arm sizing itself off this would otherwise skip on a perfectly healthy
// cluster, purely because it asked too early.
func schedSlots(t *testing.T, vpc clnt.Observer) int {
	deadline := time.Now().Add(SchedSlotsWait)
	for {
		ss, err := vpc.SchedStats()
		if err == nil && ss.GetSlots() > 0 {
			return int(ss.GetSlots())
		}
		if time.Now().After(deadline) {
			assert.Fail(t, "scheduler never reported its slot count",
				"waited %v; last err %v", SchedSlotsWait, err)
			return 0
		}
		time.Sleep(VPSampleInterval)
	}
}

// TestHPSearchValueProcsSlack is the under-utilisation arm: fewer trials than
// the cluster has slots, so nothing has to be pruned and the only question is
// whether the search widens to use what it was given.
//
// A scheduler that only ever shrinks away from a limit passes every existing
// arm and fails this one, which is exactly the gap it exists to close. Sizing
// off the reported slot count rather than a constant keeps the arm meaningful
// on a bigger host, where a fixed 3 would stop being slack and start being
// trivially satisfiable.
func TestHPSearchValueProcsSlack(t *testing.T) {
	f := newHPSearchVPFixture(t)
	if f == nil {
		return
	}
	defer f.shutdown()

	slots := schedSlots(t, f.vpc)
	if !assert.True(t, slots >= 2, "Need at least 2 slots to have slack, got %d", slots) {
		return
	}
	// One fewer trial than there are slots, so even with every trial running
	// the cluster still has room. Anything less than full width here is the
	// scheduler declining capacity, not running out of it.
	f.cfg.NConfigs = slots - 1

	ctn := startContention(t, f.sc)
	defer ctn.release()

	db.DPrintf(db.ALWAYS, "TestHPSearchValueProcsSlack: slots=%d NConfigs=%d MaxIters=%d IterDur=%v",
		slots, f.cfg.NConfigs, f.cfg.MaxIters, f.cfg.IterDur)

	base, err := runJob(f.sc, f.cfg)
	if !assert.Nil(t, err, "Error runJob (baseline): %v", err) {
		return
	}
	baseline := hpsearch.Analyze(base, f.cfg)

	live, trace, ok := f.runVP(t, "HPSearch value-procs slack")
	if !ok {
		return
	}

	db.DPrintf(db.ALWAYS, "HPSearch value-procs slack: %.2f core-s of %.2f, best-all %.3f, best-kept %.3f, quality lost %.3f, width max=%d of %d configs (%d slots)",
		live.CoreSeconds, baseline.ActualCoreSeconds, baseline.BestQualityAll, live.BestQuality,
		baseline.BestQualityAll-live.BestQuality, trace.maxRunning, f.cfg.NConfigs, slots)

	// The claim: given more slots than trials, every trial runs. Sampling can
	// miss a brief peak, so this is asserted against what was observed rather
	// than against a count derived from the tree.
	assert.Equal(t, f.cfg.NConfigs, trace.maxRunning,
		"With %d slots and only %d trials, every trial should have run concurrently", slots, f.cfg.NConfigs)

	// Quality is reported, not asserted, and the reason is worth stating
	// because it looks like a missing check.
	//
	// Slack lets every trial *run*; it does not let every trial *finish*. A
	// Select(1, N) is satisfied the moment one child completes, and the rest
	// are stopped however much capacity is going spare -- so the trial the
	// application gets to select from is whichever finished first, and with N
	// identical-length trials started together that is a race, not a ranking.
	// Demanding the best trial here would be demanding something the tree's
	// own structure cannot deliver at any amount of slack.
	//
	// What that gap measures is therefore the cost of resolving a search by
	// completion order, and it is the same defect the contended arm reports as
	// a 0.190 quality loss -- visible here at a scale where nothing was
	// contended and nothing needed pruning at all.
	db.DPrintf(db.ALWAYS, "HPSearch value-procs slack: %d of %d trials finished; quality lost to completion-order resolution %.3f",
		f.cfg.NConfigs-live.NPruned, f.cfg.NConfigs, baseline.BestQualityAll-live.BestQuality)
}

// TestHPSearchValueProcsInflatedTrial is the first negative control: one trial
// multiplies its reported score, and the question is what the honest ones lose.
//
// The defence the design offers is that a Score is opaque and only ever
// compared inside one Select, so a liar can win its own node and nothing else.
// That bounds the damage at "the liar is kept" -- which for a Select(1, N) is
// the whole result, so the bound is worth measuring rather than asserting. The
// liar's returned curve is honest, so quality lost is the real cost.
func TestHPSearchValueProcsInflatedTrial(t *testing.T) {
	f := newHPSearchVPFixture(t)
	if f == nil {
		return
	}
	defer f.shutdown()

	ctn := startContention(t, f.sc)
	defer ctn.release()

	base, err := runJob(f.sc, f.cfg)
	if !assert.Nil(t, err, "Error runJob (baseline): %v", err) {
		return
	}
	baseline := hpsearch.Analyze(base, f.cfg)

	// Honest run first, as the control: the same seeds, the same curves, and
	// the only difference is that nobody inflates.
	honest, _, ok := f.runVP(t, "HPSearch value-procs honest")
	if !ok {
		return
	}

	// Then the same search with one trial inflating. The worst config to hand
	// the advantage to is the one that would otherwise finish last, so this
	// picks by asymptote rather than by index.
	f.cfg.InflateConfig = worstConfig(base)
	f.cfg.InflateFactor = HPSearchInflateFactor
	liar, _, ok := f.runVP(t, "HPSearch value-procs inflated")
	if !ok {
		return
	}

	honestLost := baseline.BestQualityAll - honest.BestQuality
	liarLost := baseline.BestQualityAll - liar.BestQuality
	db.DPrintf(db.ALWAYS, "HPSearch value-procs inflated trial: config %d inflating x%.1f; quality lost honest %.3f -> inflated %.3f (cost of the liar %.3f)",
		f.cfg.InflateConfig, HPSearchInflateFactor, honestLost, liarLost, liarLost-honestLost)

	// The bound the design claims: a liar can cost at most the difference
	// between the best trial and itself, because it can only win its own node.
	// It cannot make the search return something worse than the trial it
	// inflated.
	worst := base[f.cfg.InflateConfig]
	assert.True(t, liar.BestQuality >= curveBest(worst),
		"An inflating trial should at worst win with its own true quality (%.3f), got %.3f",
		curveBest(worst), liar.BestQuality)
}

// TestHPSearchValueProcsSilentTrial is the second negative control: one trial
// reports nothing until it finishes.
//
// A scheduler that values an unreported trial at nothing starves it forever,
// which would make "report often" a correctness requirement rather than an
// optimization. A scheduler that values it on a peer's tangent, or on its own
// prior, gives it a chance to run. This arm reports which of those happened.
func TestHPSearchValueProcsSilentTrial(t *testing.T) {
	f := newHPSearchVPFixture(t)
	if f == nil {
		return
	}
	defer f.shutdown()

	ctn := startContention(t, f.sc)
	defer ctn.release()

	// The silent trial is the best one, so if it starves the search loses the
	// most it possibly can, and quality lost measures exactly that.
	base, err := runJob(f.sc, f.cfg)
	if !assert.Nil(t, err, "Error runJob (baseline): %v", err) {
		return
	}
	baseline := hpsearch.Analyze(base, f.cfg)
	f.cfg.SilentConfig = bestConfig(base)

	live, trace, ok := f.runVP(t, "HPSearch value-procs silent")
	if !ok {
		return
	}

	db.DPrintf(db.ALWAYS, "HPSearch value-procs silent trial: config %d silent; best-all %.3f, best-kept %.3f, quality lost %.3f, width max=%d",
		f.cfg.SilentConfig, baseline.BestQualityAll, live.BestQuality,
		baseline.BestQualityAll-live.BestQuality, trace.maxRunning)

	// The search still has to terminate with something the application can
	// select from -- a silent trial must not be able to wedge the tree.
	assert.True(t, live.NPruned < f.cfg.NConfigs,
		"A silent trial should not stop the search finishing any trial at all")
}

// TestHPSearchValueProcsOverhead is the uncontended-overhead control: what
// value-procs costs when there is nothing to arbitrate.
//
// Reported as its own arm rather than as the top row of a contention sweep,
// because "it is free when idle" is a claim in its own right and a sweep row
// invites reading it as a contention effect. This arm deliberately ignores
// -contention_free_mb: it is about the uncontended case.
func TestHPSearchValueProcsOverhead(t *testing.T) {
	f := newHPSearchVPFixture(t)
	if f == nil {
		return
	}
	defer f.shutdown()

	// No contention injected at all, whatever the sweep asked for: this arm
	// never calls startContention.

	start := time.Now()
	base, err := runJob(f.sc, f.cfg)
	if !assert.Nil(t, err, "Error runJob (baseline): %v", err) {
		return
	}
	baseDur := time.Since(start)
	baseline := hpsearch.Analyze(base, f.cfg)

	start = time.Now()
	live, trace, ok := f.runVP(t, "HPSearch value-procs overhead")
	if !ok {
		return
	}
	vpDur := time.Since(start)

	db.DPrintf(db.ALWAYS, "HPSearch value-procs overhead (uncontended): baseline %v / %.2f core-s, value-procs %v / %.2f core-s, quality lost %.3f, width max=%d mean=%.2f, pressure mean=%.3f",
		baseDur, baseline.ActualCoreSeconds, vpDur, live.CoreSeconds,
		baseline.BestQualityAll-live.BestQuality, trace.maxRunning, trace.meanRunning, trace.meanPressure)

	// The one thing that must hold: scheduling a search through valuesched
	// cannot cost more compute than running every trial to completion.
	assert.True(t, live.CoreSeconds <= baseline.ActualCoreSeconds,
		"Value-procs used more compute (%.2f core-s) than running everything (%.2f core-s)",
		live.CoreSeconds, baseline.ActualCoreSeconds)
}

// curveBest is the highest score a curve reached.
func curveBest(c *hpsearch.Curve) float64 {
	best := 0.0
	for _, s := range c.Scores {
		if s > best {
			best = s
		}
	}
	return best
}

// bestConfig and worstConfig name the extremes of a baseline run, so an arm
// can hand its perturbation to the trial where it does the most damage rather
// than to an arbitrary index.
func bestConfig(curves []*hpsearch.Curve) int {
	id, best := 0, -1.0
	for _, c := range curves {
		if v := curveBest(c); v > best {
			id, best = c.ConfigId, v
		}
	}
	return id
}

func worstConfig(curves []*hpsearch.Curve) int {
	id, worst := 0, 2.0
	for _, c := range curves {
		if v := curveBest(c); v < worst {
			id, worst = c.ConfigId, v
		}
	}
	return id
}

// --- the squeeze arm --------------------------------------------------------

const (
	// SqueezeBurnMillis is how long each burner occupies a core. Long enough
	// to outlast the squeeze phase; the tree is cancelled rather than waited
	// for, so overrunning costs nothing.
	SqueezeBurnMillis = 600_000

	// SqueezeIters is what a trial in this arm runs for. Much longer than the
	// default, because the phases below each have to outlast ConfirmFor: a
	// shrink has to be proposed continuously before it is applied, and a
	// search that finished first would leave the squeeze unobserved.
	SqueezeIters = 4000

	// SqueezeSettle bounds how long a phase may take to land.
	SqueezeSettle = 90 * time.Second
)

// treeWidth is how many attempts a tree is running right now, or -1 if it
// cannot be read.
func treeWidth(vpc clnt.Observer, tid string) int {
	st, err := vpc.Status(tid)
	if err != nil {
		return -1
	}
	return int(st.NRunning)
}

// awaitWidth waits for a tree's running count to satisfy ok, and reports
// whether it did. It fails the test on timeout, quoting what it saw instead.
//
// Width is waited for rather than slept for because the delays are not
// constants: a contraction has to clear ConfirmFor, and the reports driving it
// arrive at whatever rate the trials manage under whatever share of the
// machine they have at the time.
func awaitWidth(t *testing.T, vpc clnt.Observer, tid, why string, ok func(int) bool) bool {
	deadline := time.Now().Add(SqueezeSettle)
	last := -1
	for time.Now().Before(deadline) {
		if last = treeWidth(vpc, tid); last >= 0 && ok(last) {
			db.DPrintf(db.ALWAYS, "squeeze: %v -- %v is running %d", why, tid, last)
			return true
		}
		time.Sleep(VPSampleInterval)
	}
	assert.Fail(t, "tree never reached the expected width",
		"%v: last saw %d after %v", why, last, SqueezeSettle)
	return false
}

// TestHPSearchValueProcsSqueeze is the half of the sizing claim that memory
// contention cannot reach: what a search gives up when another tree takes a
// share of the machine, and what it takes back when that tree goes away.
//
// The competitor is a tree rather than load outside the scheduler, and that is
// the point. Slots is what the machines can run, so an external squeeze
// reaches width only through queue delay, which withholds growth and never
// forces a shed -- it cannot narrow a search that has already settled. A
// second tree divides the same ledger, which can.
//
// It also sidesteps the contention harness's schedule entirely: the squeeze
// arrives and leaves when this test says so, with the sampler already running,
// so both transitions are inside the observed window by construction.
func TestHPSearchValueProcsSqueeze(t *testing.T) {
	f := newHPSearchVPFixture(t)
	if f == nil {
		return
	}
	defer f.shutdown()

	slots := schedSlots(t, f.vpc)
	if !assert.True(t, slots >= 2, "Need at least 2 slots to divide, got %d", slots) {
		return
	}
	f.cfg.MaxIters = SqueezeIters

	smp := startVPSampler(f.vpc)
	defer smp.summarize()

	j, err := hpsearch.StartValueProcsJob(f.vpc, f.cfg)
	if !assert.Nil(t, err, "Error StartValueProcsJob: %v", err) {
		return
	}
	defer f.vpc.Cancel(j.TID())
	db.DPrintf(db.ALWAYS, "TestHPSearchValueProcsSqueeze: slots=%d NConfigs=%d MaxIters=%d",
		slots, f.cfg.NConfigs, f.cfg.MaxIters)

	full := func(n int) bool { return n == slots }

	// Alone, the search narrows to what the machines can run.
	if !awaitWidth(t, f.vpc, j.TID(), "uncontended", full) {
		return
	}
	alone := time.Since(smp.start)

	// A competitor arrives. It is elastic -- a quorum of one over as many
	// burners as there are slots -- so both trees can be sized. An inelastic
	// one, needing every child, would drive the search to its own quorum
	// instead of to its share, which is a different claim.
	burners := make([]*clnt.WorkNode, slots)
	for i := range burners {
		p := proc.NewProc("vpburn", []string{strconv.Itoa(SqueezeBurnMillis)})
		burners[i] = clnt.Leaf(p).WithLabel(fmt.Sprintf("burn-%d", i))
	}
	burnRoot, err := clnt.Select(1, burners...)
	if !assert.Nil(t, err, "Error Select: %v", err) {
		return
	}
	burnTid := "vpburn-" + sp.GenPid("").String()
	if _, err := f.vpc.Submit(burnTid, "vpburn", burnRoot); !assert.Nil(t, err, "Error Submit: %v", err) {
		return
	}

	// At most its share. Stated as a bound rather than as the exact half,
	// because which tree lands on the odd slot depends on the order reports
	// arrive in, and the claim is that the search gives capacity up rather
	// than that it gives up a particular slot.
	squeezed := awaitWidth(t, f.vpc, j.TID(), "squeezed by a second tree",
		func(n int) bool { return n > 0 && n <= slots/2 })
	squeezedAt := time.Since(smp.start)

	// And takes it back when the competitor goes.
	assert.Nil(t, f.vpc.Cancel(burnTid), "Error Cancel")
	released := awaitWidth(t, f.vpc, j.TID(), "released", full)

	trace := smp.reportTrace("HPSearch value-procs squeeze")
	db.DPrintf(db.ALWAYS, "HPSearch value-procs squeeze: settled at %d after %v, squeezed to %d by %v, %d burners; trace %v",
		slots, alone.Round(time.Second), slots/2, squeezedAt.Round(time.Second), len(burners), trace)

	// Stated as the two transitions rather than as levels, because a run that
	// never moved and a run that moved and moved back reduce to the same
	// summary. Both are asserted by awaitWidth above; this records that the
	// pair of them happened in one run.
	assert.True(t, squeezed && released,
		"the search should give up half the machine to a second tree and take it back")
}
