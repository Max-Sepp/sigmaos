package adapter

import (
	"errors"
	"sync"
	"testing"
	"time"

	procapi "sigmaos/api/proc"
	"sigmaos/proc"
	sp "sigmaos/sigmap"
	"sigmaos/util/spstats"
	"sigmaos/valueprocs"
	"sigmaos/valueprocs/gate"
	"sigmaos/valueprocs/policy"
)

// The two ends of the adapter, asserted where they cost nothing: it drives
// the real proc API, and the gate is what it reports to.
var (
	_ procapi.ProcAPI = (*fakeProcAPI)(nil)
	_ EventSink       = (*gate.Gate)(nil)
	_ EventSink       = (*sink)(nil)
)

// fakeProcAPI stands in for SigmaOS. It is the first procapi.ProcAPI fake in
// the repo, and it exists so that the adapter's obligation — exactly one
// terminal event per attempt, always — can be tested against a platform that
// misbehaves on demand, which a real one will not do to order.
type fakeProcAPI struct {
	mu sync.Mutex

	spawned []*proc.Proc
	evicted []sp.Tpid
	waited  []sp.Tpid // every WaitExit, so a repeat is visible

	spawnErr error
	startErr error

	// startHold, when set, parks WaitStart until it is closed, which is how a
	// test arranges for an attempt to be resolved while the goroutine that
	// would report it started is still in flight.
	startHold chan struct{}

	// evictErr is returned by every Evict. A test that wants a stop request
	// to be undeliverable sets it.
	evictErr error

	// exits gives each pid its outcome. A pid with no entry blocks in
	// WaitExit until one arrives, which is how a test holds an attempt open.
	exits map[sp.Tpid]chan exitOf
}

type exitOf struct {
	st  *proc.Status
	err error
}

func newFakeProcAPI() *fakeProcAPI {
	return &fakeProcAPI{exits: make(map[sp.Tpid]chan exitOf)}
}

func (f *fakeProcAPI) exitCh(pid sp.Tpid) chan exitOf {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch, ok := f.exits[pid]
	if !ok {
		ch = make(chan exitOf, 1)
		f.exits[pid] = ch
	}
	return ch
}

// exit releases whichever attempt is waiting on pid.
func (f *fakeProcAPI) exit(pid sp.Tpid, st *proc.Status, err error) {
	f.exitCh(pid) <- exitOf{st, err}
}

func (f *fakeProcAPI) Spawn(p *proc.Proc) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.spawnErr != nil {
		return f.spawnErr
	}
	f.spawned = append(f.spawned, p)
	return nil
}

func (f *fakeProcAPI) WaitStart(pid sp.Tpid) error {
	f.mu.Lock()
	err, hold := f.startErr, f.startHold
	f.mu.Unlock()
	if hold != nil {
		<-hold
	}
	return err
}

// holdStart parks every WaitStart until the returned function is called.
func (f *fakeProcAPI) holdStart() func() {
	ch := make(chan struct{})
	f.mu.Lock()
	f.startHold = ch
	f.mu.Unlock()
	return func() { close(ch) }
}

func (f *fakeProcAPI) WaitExit(pid sp.Tpid) (*proc.Status, error) {
	f.mu.Lock()
	f.waited = append(f.waited, pid)
	f.mu.Unlock()
	e := <-f.exitCh(pid)
	return e.st, e.err
}

func (f *fakeProcAPI) Evict(pid sp.Tpid) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evicted = append(f.evicted, pid)
	return f.evictErr
}

func (f *fakeProcAPI) Started() error              { return nil }
func (f *fakeProcAPI) Exited(status *proc.Status)  {}
func (f *fakeProcAPI) WaitEvict(pid sp.Tpid) error { return nil }

func (f *fakeProcAPI) Stats() *spstats.TcounterSnapshot { return nil }

func (f *fakeProcAPI) nSpawned() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.spawned)
}

func (f *fakeProcAPI) nEvicted() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.evicted)
}

// pidOf returns the pid of the i'th spawn, waiting for it to happen.
func (f *fakeProcAPI) pidOf(t *testing.T, i int) sp.Tpid {
	t.Helper()
	eventually(t, func() bool { return f.nSpawned() > i }, "a proc was spawned")
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spawned[i].GetPid()
}

// procForNode finds the proc spawned for a node, by the identity the adapter
// injected into its environment. Spawn order does not follow tree order, so
// an index is the wrong way to ask.
func (f *fakeProcAPI) procForNode(t *testing.T, node string) *proc.Proc {
	t.Helper()
	var found *proc.Proc
	eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, p := range f.spawned {
			if p.Env[valueprocs.ENV_NODE] == node {
				found = p
				return true
			}
		}
		return false
	}, "a proc was spawned for node "+node)
	return found
}

func (f *fakeProcAPI) procOf(t *testing.T, i int) *proc.Proc {
	t.Helper()
	eventually(t, func() bool { return f.nSpawned() > i }, "a proc was spawned")
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spawned[i]
}

// event is one call the adapter made into the layer above.
type event struct {
	kind string // "started", "completed", "stopped", "failed"
	ref  policy.RunRef
	data []byte
	fail policy.FailureKind
	msg  string
}

// sink records the terminal-event contract so a test can assert on it
// directly rather than on its consequences.
type sink struct {
	mu sync.Mutex
	ev []event
}

func (s *sink) add(e event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ev = append(s.ev, e)
}

func (s *sink) OnRunStarted(ref policy.RunRef) { s.add(event{kind: "started", ref: ref}) }

func (s *sink) OnRunCompleted(ref policy.RunRef, r []byte) {
	s.add(event{kind: "completed", ref: ref, data: r})
}

func (s *sink) OnRunStopped(ref policy.RunRef, p []byte) {
	s.add(event{kind: "stopped", ref: ref, data: p})
}

func (s *sink) OnRunFailed(ref policy.RunRef, k policy.FailureKind, msg string) {
	s.add(event{kind: "failed", ref: ref, fail: k, msg: msg})
}

func (s *sink) events() []event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]event(nil), s.ev...)
}

// terminals returns the terminal events only. Counting these is how the
// adapter's central invariant is checked.
func (s *sink) terminals() []event {
	out := []event{}
	for _, e := range s.events() {
		if e.kind != "started" {
			out = append(out, e)
		}
	}
	return out
}

func (s *sink) count(kind string) int {
	n := 0
	for _, e := range s.events() {
		if e.kind == kind {
			n++
		}
	}
	return n
}

func testTuning() SigmaOSTuning {
	return SigmaOSTuning{
		StopRetries: 2,
		StopBackoff: time.Millisecond,
		StopTimeout: 50 * time.Millisecond,
	}
}

func newExec(t *testing.T, tuning SigmaOSTuning) (*Exec, *fakeProcAPI, *sink) {
	t.Helper()
	f, s := newFakeProcAPI(), &sink{}
	e := NewExec(f, s, tuning)
	t.Cleanup(e.Close)
	return e, f, s
}

// template returns a workload the adapter will accept.
func template(t *testing.T, program string, args ...string) *ProcTemplate {
	t.Helper()
	p := proc.NewProc(program, args)
	tm, err := NewProcTemplate(p)
	if err != nil {
		t.Fatalf("NewProcTemplate: %v", err)
	}
	return tm
}

func ref(node string, run policy.RunID) policy.RunRef {
	return policy.RunRef{Tree: "t", Node: policy.NodeID(node), Run: run}
}

// eventually polls, because every platform call runs on its own goroutine, so
// a decision and its effect are never simultaneous.
func eventually(t *testing.T, f func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting: %s", msg)
}

// consistently asserts f holds for a while, which is how "no second event
// ever arrives" is checked.
func consistently(t *testing.T, d time.Duration, f func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !f() {
			t.Fatalf("stopped holding: %s", msg)
		}
		time.Sleep(time.Millisecond)
	}
}

var errPlatform = errors.New("platform said no")
