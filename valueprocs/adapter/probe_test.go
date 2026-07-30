package adapter

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	mschedproto "sigmaos/sched/msched/proto"
	sp "sigmaos/sigmap"
	"sigmaos/valueprocs/policy"
)

// machine describes one kernel's load, as msched would report it.
func machine(freeMB, totalMB uint32, cpu int64, cores int32) *mschedproto.GetMSchedLoadRep {
	return &mschedproto.GetMSchedLoadRep{
		MemFreeMB: freeMB, MemTotalMB: totalMB, CpuUtil: cpu, NCores: cores,
	}
}

func fleet(ms ...*mschedproto.GetMSchedLoadRep) map[string]*mschedproto.GetMSchedLoadRep {
	out := make(map[string]*mschedproto.GetMSchedLoadRep, len(ms))
	for i, m := range ms {
		out[string(rune('a'+i))] = m
	}
	return out
}

// fakeLoads is a cluster that answers however a test needs it to.
type fakeLoads struct {
	mu   sync.Mutex
	fl   map[string]*mschedproto.GetMSchedLoadRep
	err  error
	n    int
	qerr error
	q    map[sp.Trealm]int
}

func (f *fakeLoads) MSchedLoad() (map[string]*mschedproto.GetMSchedLoadRep, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	return f.fl, f.err
}

func (f *fakeLoads) GetQueueStats(nsample int) (map[sp.Trealm]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.q, f.qerr
}

func (f *fakeLoads) set(fl map[string]*mschedproto.GetMSchedLoadRep, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fl, f.err = fl, err
}

func (f *fakeLoads) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

func newProbe(fl *fakeLoads) *Probe {
	return &Probe{loads: fl, queues: fl, realm: "r1", over: 1, nsample: 2, quit: make(chan struct{})}
}

// TestMemoryAloneCannotSeeABusyCluster is the reason the fold takes a max.
//
// Best-effort admission gates on free memory, and best-effort procs routinely
// reserve none, so a fleet pinning every core reports all its memory free.
// These numbers are what a real kernel measured under test.
func TestMemoryAloneCannotSeeABusyCluster(t *testing.T) {
	o, ok := fold(fleet(machine(15807, 15807, 50, 4)), 1)
	assert.True(t, ok)

	assert.Zero(t, o.Components["mem"], "memory reports the machine untouched")
	assert.InDelta(t, 0.5, o.Components["cpu"], 0.001, "the CPU reports it half gone")
	assert.InDelta(t, 0.5, o.Busy, 0.001, "the max is what saves the reading")

	// The counterfactual: a weighted average of the two would have called a
	// half-consumed machine a quarter busy, and the scheduler would have
	// spent slack that does not exist.
	assert.Greater(t, o.Busy, (o.Components["mem"]+o.Components["cpu"])/2)
}

func TestFoldOverAFleet(t *testing.T) {
	for _, tc := range []struct {
		name string
		fl   map[string]*mschedproto.GetMSchedLoadRep
		busy float64
		slot int
	}{
		{
			name: "idle",
			fl:   fleet(machine(1000, 1000, 0, 4), machine(1000, 1000, 0, 4)),
			busy: 0, slot: 8,
		},
		{
			name: "saturated on memory",
			fl:   fleet(machine(0, 1000, 0, 2)),
			busy: 1, slot: 2,
		},
		{
			name: "saturated on cpu",
			fl:   fleet(machine(1000, 1000, 100, 2)),
			busy: 1, slot: 2,
		},
		{
			name: "either term saturating means full",
			fl:   fleet(machine(0, 1000, 5, 2)),
			busy: 1, slot: 2,
		},
		{
			name: "memory pools across machines",
			fl:   fleet(machine(0, 1000, 0, 1), machine(1000, 1000, 0, 1)),
			busy: 0.5, slot: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, ok := fold(tc.fl, 1)
			assert.True(t, ok)
			assert.InDelta(t, tc.busy, o.Busy, 0.001)
			assert.Equal(t, tc.slot, o.Slots)
		})
	}
}

func TestCpuIsWeightedByCores(t *testing.T) {
	// A big idle machine beside a small busy one. Averaging the percentages
	// would call the cluster half busy; weighting by cores says most of the
	// cluster's capacity is free, which is the question being asked.
	o, ok := fold(fleet(machine(1000, 1000, 0, 30), machine(1000, 1000, 100, 2)), 1)
	assert.True(t, ok)
	assert.InDelta(t, 2.0/32.0, o.Busy, 0.001)
}

func TestOversubscribeTurnsCoresIntoSlots(t *testing.T) {
	fl := fleet(machine(1000, 1000, 0, 4), machine(1000, 1000, 0, 4))
	o, _ := fold(fl, 1)
	assert.Equal(t, 8, o.Slots)
	o, _ = fold(fl, 2.5)
	assert.Equal(t, 20, o.Slots)

	// A nonsensical factor falls back to one slot per core rather than
	// reporting a cluster with no capacity at all.
	o, _ = fold(fl, 0)
	assert.Equal(t, 8, o.Slots)
}

func TestFoldRefusesToMeasureNothing(t *testing.T) {
	// A cluster nobody can see is not an idle cluster. Reporting zero would
	// have the scheduler run everything it has against capacity it cannot
	// confirm exists.
	_, ok := fold(nil, 1)
	assert.False(t, ok)
	_, ok = fold(fleet(machine(0, 0, 0, 0)), 1)
	assert.False(t, ok)
}

func TestPressureNeverExceedsItsBounds(t *testing.T) {
	// msched reports free memory it maintains itself and a utilization
	// sampled from the OS. Neither is guaranteed sane, and a Busy above one
	// would drive the target rule below k, where only a downstream clamp
	// rescues it. Bound the input instead.
	o, ok := fold(fleet(machine(9999, 1000, 250, 2)), 1)
	assert.True(t, ok)
	assert.GreaterOrEqual(t, o.Busy, 0.0)
	assert.LessOrEqual(t, o.Busy, 1.0)
}

func TestProbeKeepsTheLastGoodReading(t *testing.T) {
	fl := &fakeLoads{}
	p := newProbe(fl)
	defer p.Close()

	// Nothing measured yet, so there is nothing to report.
	_, ok := p.Sample()
	assert.False(t, ok)

	fl.set(fleet(machine(500, 1000, 0, 4)), nil)
	o, ok := p.Sample()
	assert.True(t, ok)
	assert.InDelta(t, 0.5, o.Busy, 0.001)

	// The cluster goes unreachable. A stale reading beats inventing one:
	// zero would say idle and one would say full, and both are guesses.
	fl.set(nil, errPlatform)
	o, ok = p.Sample()
	assert.True(t, ok)
	assert.InDelta(t, 0.5, o.Busy, 0.001, "the last good reading is held")
}

func TestProbeCarriesQueueDepthWithoutDependingOnIt(t *testing.T) {
	fl := &fakeLoads{q: map[sp.Trealm]int{"r1": 17, "r2": 3}}
	fl.set(fleet(machine(1000, 1000, 0, 4)), nil)
	p := newProbe(fl)
	defer p.Close()

	o, ok := p.Sample()
	assert.True(t, ok)

	// Sampled from random shards without normalizing by how many were
	// actually reached, so the magnitude means nothing. It is here to be
	// looked at, and Busy must not move because of it.
	assert.EqualValues(t, 17, o.Components["beQueuedSampled"])
	assert.Zero(t, o.Busy)

	// And a queue that cannot be reached does not spoil a good measurement.
	fl.qerr = errPlatform
	o, ok = p.Sample()
	assert.True(t, ok)
	assert.Zero(t, o.Busy)
}

func TestProbePushesOnItsOwnSchedule(t *testing.T) {
	fl := &fakeLoads{}
	fl.set(fleet(machine(0, 1000, 0, 4)), nil)
	p := newProbe(fl)

	var (
		mu   sync.Mutex
		seen []policy.Occupancy
	)
	p.Run(time.Millisecond, func(o policy.Occupancy) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, o)
	})

	eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) >= 3
	}, "the probe pushed repeatedly")

	mu.Lock()
	assert.InDelta(t, 1.0, seen[0].Busy, 0.001)
	mu.Unlock()

	// Close stops it.
	p.Close()
	n := fl.calls()
	time.Sleep(20 * time.Millisecond)
	assert.LessOrEqual(t, fl.calls(), n+1, "no measurements after Close")

	assert.NotPanics(t, p.Close, "Close is idempotent")
}
