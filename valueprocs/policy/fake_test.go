package policy

import (
	"fmt"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// work is a Workload that is nothing but its name.
type work string

func (w work) Name() string { return string(w) }

// call is one recorded platform call. Recording both verbs in one list is
// what lets a test assert on ordering between starts and stops.
type call struct {
	start  bool
	ref    RunRef
	launch Launch
	why    StartReason
	stop   StopReason
}

func (c call) String() string {
	if c.start {
		return fmt.Sprintf("start %v %v", c.ref, c.why.Kind)
	}
	return fmt.Sprintf("stop %v %v", c.ref, c.stop.Kind)
}

type fake struct{ calls []call }

func (f *fake) Start(ref RunRef, l Launch, why StartReason) {
	f.calls = append(f.calls, call{start: true, ref: ref, launch: l, why: why})
}

func (f *fake) Stop(ref RunRef, why StopReason) {
	f.calls = append(f.calls, call{ref: ref, stop: why})
}

func (f *fake) reset() { f.calls = nil }

func (f *fake) starts() []call {
	var out []call
	for _, c := range f.calls {
		if c.start {
			out = append(out, c)
		}
	}
	return out
}

func (f *fake) stops() []call {
	var out []call
	for _, c := range f.calls {
		if !c.start {
			out = append(out, c)
		}
	}
	return out
}

func (f *fake) trace() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.String())
	}
	return out
}

// testConfig disables smoothing and confirmation so that a test states a
// pressure and sees its consequence. The tests that exist to exercise those
// two put them back.
func testConfig() Config {
	c := DefaultConfig()
	c.EWMAAlpha = 1
	c.ConfirmFor = 0
	return c
}

// proportionalConfig is testConfig sizing on the occupancy reading, so that a
// test of the arm being argued against differs from its counterpart in exactly
// one field. It is also what the tests of the value model use where the
// property they pin is about admission rather than capacity: under the ledger
// rule slack fills a free slot before any comparison is reached, so a node
// with room to spare cannot demonstrate a choice between candidates.
func proportionalConfig() Config {
	c := testConfig()
	c.Sizing = SizingProportional
	return c
}

func newSched(cfg Config) (*Scheduler, *fake) {
	f := &fake{}
	return NewScheduler(cfg, f, nil, nil), f
}

// evenSplit is the smallest arbiter that divides anything, so that a test of
// what the scheduler does with a share does not also depend on how a real
// arbiter decides one. The remainder goes to the trees named first.
type evenSplit struct{}

func (evenSplit) Arbitrate(trees []TreeView, c Capacity) []TreeShare {
	if len(trees) == 0 || c.Slots <= 0 {
		return nil
	}
	share, extra := c.Slots/len(trees), c.Slots%len(trees)
	out := make([]TreeShare, 0, len(trees))
	for i, t := range trees {
		n := share
		if i < extra {
			n++
		}
		out = append(out, TreeShare{Tree: t.ID, Slots: n})
	}
	return out
}

func newSchedArb(cfg Config, arb Arbiter) (*Scheduler, *fake) {
	f := &fake{}
	return NewScheduler(cfg, f, arb, nil), f
}

// sized reports a cluster of a stated size that is not otherwise busy, so a
// test can put a ceiling on capacity without also raising pressure.
func sized(s *Scheduler, now time.Time, slots int) {
	apply(s.OnOccupancy(now, Occupancy{Slots: slots}))
}

// stopped delivers the terminal event for every outstanding stop, which is
// what the adapter would do once the platform confirmed them.
func stopped(s *Scheduler, f *fake, now time.Time) {
	refs := make([]RunRef, 0, len(f.calls))
	for _, c := range f.stops() {
		refs = append(refs, c.ref)
	}
	for _, r := range refs {
		apply(s.OnRunStopped(now, r, nil))
	}
}

func apply(e Effect) {
	if e != nil {
		e()
	}
}

func leafG(t *testing.T, name string) Group {
	t.Helper()
	g, err := Leaf(work(name))
	if err != nil {
		t.Fatalf("Leaf(%q): %v", name, err)
	}
	return g
}

func leavesG(t *testing.T, n int) []Group {
	t.Helper()
	out := make([]Group, n)
	for i := range out {
		out[i] = leafG(t, fmt.Sprintf("w%d", i))
	}
	return out
}

func selG(t *testing.T, k int, cs ...Group) Group {
	t.Helper()
	g, err := Select(k, cs...)
	if err != nil {
		t.Fatalf("Select(%d): %v", k, err)
	}
	return g
}

func submit(t *testing.T, s *Scheduler, now time.Time, id TreeID, root Group) {
	t.Helper()
	_, eff, err := s.SubmitTree(now, TreeSpec{ID: id, Root: root})
	if err != nil {
		t.Fatalf("SubmitTree(%q): %v", id, err)
	}
	apply(eff)
}

// startQueued reports every outstanding start as running, which is what the
// adapter would do once the platform placed the work.
func startQueued(s *Scheduler, f *fake, now time.Time) {
	refs := make([]RunRef, 0, len(f.calls))
	for _, c := range f.starts() {
		refs = append(refs, c.ref)
	}
	for _, r := range refs {
		apply(s.OnRunStarted(now, r))
	}
}

// busy sets platform occupancy without imposing a slot ceiling, so that a
// test can vary pressure alone.
func busy(s *Scheduler, now time.Time, b float64) {
	apply(s.OnOccupancy(now, Occupancy{Busy: b}))
}

func nodeView(t *testing.T, s *Scheduler, id TreeID, nid NodeID) NodeView {
	t.Helper()
	v, ok := s.TreeView(id)
	if !ok {
		t.Fatalf("no tree %q", id)
	}
	for _, n := range v.Nodes {
		if n.ID == nid {
			return n
		}
	}
	t.Fatalf("no node %q in tree %q", nid, id)
	return NodeView{}
}

func refOf(id TreeID, nid NodeID, run RunID) RunRef {
	return RunRef{Tree: id, Node: nid, Run: run}
}
