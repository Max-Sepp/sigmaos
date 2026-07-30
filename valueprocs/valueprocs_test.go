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
)

// runForever is long enough that a proc reaching the end of it on its own
// would fail the test by timing out rather than by passing quietly.
const runForever = 60 * 1000

// runBriefly is what the bypass case waits out, so a test that proves
// eviction was ignored still finishes.
const runBriefly = 4 * 1000

func spawnEvictee(t *testing.T, ts *test.RealmTstate, mode string, ms int) (sp.Tpid, bool) {
	t.Helper()
	p := proc.NewProc("vproc-evictee", []string{mode, strconv.Itoa(ms)})
	if err := ts.Spawn(p); !assert.Nil(t, err, "Spawn: %v", err) {
		return "", false
	}
	if err := ts.WaitStart(p.GetPid()); !assert.Nil(t, err, "WaitStart: %v", err) {
		return "", false
	}
	return p.GetPid(), true
}

// TestAutoExitOnEvict is the test that certifies this layer can reclaim
// anything at all. Every stop the scheduler issues is an eviction, and an
// eviction only ends a proc that waits for it -- so if this regresses, every
// stop in the system silently becomes a no-op.
func TestAutoExitOnEvict(t *testing.T) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{test.REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return
	}
	defer mrts.Shutdown()
	ts := mrts.GetRealm(test.REALM1)

	pid, ok := spawnEvictee(t, ts, "auto", runForever)
	if !ok {
		return
	}

	start := time.Now()
	assert.Nil(t, ts.Evict(pid), "Evict")

	status, err := ts.WaitExit(pid)
	elapsed := time.Since(start)
	if !assert.Nil(t, err, "WaitExit: %v", err) {
		return
	}
	db.DPrintf(db.TEST, "auto: exited after %v with %v", elapsed, status)

	// The status matters as much as the exit. Reporting before terminating is
	// what lets msched see a deliberate eviction; had the proc exited first,
	// msched would have synthesized a crash status on its behalf and a
	// routine reclamation would read as a failure.
	if assert.NotNil(t, status, "no status") {
		assert.True(t, status.IsStatusEvicted(), "status was %v, want EVICTED", status)
	}
	assert.Less(t, elapsed, 20*time.Second,
		"the proc had %v ms of work left, so it stopped because it was evicted", runForever)
}

// TestEvictionIsInertWithoutIt is the contrast, and the reason the one line
// above is worth landing on its own. A proc that never waits on its eviction
// runs to completion holding resources its parent has already given away --
// which is what MapReduce does today with its losing speculative attempts.
func TestEvictionIsInertWithoutIt(t *testing.T) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{test.REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return
	}
	defer mrts.Shutdown()
	ts := mrts.GetRealm(test.REALM1)

	pid, ok := spawnEvictee(t, ts, "bypass", runBriefly)
	if !ok {
		return
	}

	start := time.Now()
	// Evict returns nil, which says only that the flag was set. It says
	// nothing about whether the proc stopped, and here it did not.
	assert.Nil(t, ts.Evict(pid), "Evict")

	status, err := ts.WaitExit(pid)
	elapsed := time.Since(start)
	if !assert.Nil(t, err, "WaitExit: %v", err) {
		return
	}
	db.DPrintf(db.TEST, "bypass: exited after %v with %v", elapsed, status)

	if assert.NotNil(t, status, "no status") {
		assert.True(t, status.IsStatusOK(),
			"status was %v: the eviction should have been ignored", status)
	}
	assert.Greater(t, elapsed, time.Duration(runBriefly/2)*time.Millisecond,
		"the proc ran on after being evicted rather than stopping")
}
