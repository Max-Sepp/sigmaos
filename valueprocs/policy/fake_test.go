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

// testConfig disables smoothing and dwell so that a test states a pressure
// and sees its consequence. The tests that exist to exercise those two put
// them back.
func testConfig() Config {
	c := DefaultConfig()
	c.EWMAAlpha = 1
	c.MinDwell = 0
	return c
}

func newSched(cfg Config) (*Scheduler, *fake) {
	f := &fake{}
	return NewScheduler(cfg, f, nil, nil), f
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
