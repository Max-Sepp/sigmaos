package benchmarks_test

// Tests for the real-memory contention injector.
//
// These assert against the host's own /proc/meminfo rather than against
// anything the fillers report, because a filler that believes it took memory
// it did not take is exactly the failure worth catching. They are also the
// gate before a sweep: a level that cannot be delivered safely on this host
// should fail here, in seconds, rather than in hour six of a run.

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/sigmaclnt"
	sp "sigmaos/sigmap"
	"sigmaos/test"
	"sigmaos/util/linux/mem"
)

// FillerSettleTime is how long to let the kernel's MemAvailable estimate catch
// up after memory is taken or given back.
const FillerSettleTime = 2 * time.Second

func availMB() int { return int(mem.GetAvailableMem()) }

// spawnFiller starts one filler and waits for its memory to be resident.
func spawnFiller(t *testing.T, sc *sigmaclnt.SigmaClnt, targetFreeMB, floorMB int, ttl time.Duration) *proc.Proc {
	p := proc.NewProc(ContentionFillerBin, []string{
		fmt.Sprintf("%v", targetFreeMB),
		fmt.Sprintf("%v", floorMB),
		fmt.Sprintf("%v", contentionChunkMB),
		ttl.String(),
		ContentionWatchPeriod.String(),
	})
	if !assert.Nil(t, sc.Spawn(p), "Err Spawn filler") {
		return nil
	}
	if !assert.Nil(t, sc.WaitStart(p.GetPid()), "Err WaitStart filler") {
		return nil
	}
	return p
}

func TestMemFillerBasic(t *testing.T) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return
	}
	defer mrts.Shutdown()
	sc := mrts.GetRealm(REALM1).SigmaClnt

	if swap := mem.GetSwapTotal(); swap > 0 {
		t.Skipf("swap is on (%vMB); the filler refuses to squeeze and this test cannot mean anything", swap)
	}

	before := availMB()
	// Leave a little over a gigabyte less free than there is now, so the test
	// takes a small, safe bite rather than driving the host anywhere near the
	// floor.
	target := before - 1024
	if target < AbsMinFreeMB {
		t.Skipf("only %vMB available; not enough headroom above the %vMB floor to test a squeeze", before, AbsMinFreeMB)
	}

	p := spawnFiller(t, sc, target, contentionAbortFloorMB, 5*time.Minute)
	if p == nil {
		return
	}
	during := availMB()
	db.DPrintf(db.ALWAYS, "TestMemFillerBasic: before=%vMB target=%vMB during=%vMB", before, target, during)

	// WaitStart returning means the ramp finished, so the memory is already
	// resident and this needs no settling time.
	assert.InDelta(t, target, during, ContentionDeliveryTolMB,
		"Filler should have left ~%vMB free, left %vMB", target, during)

	releaseContention(sc, []*proc.Proc{p})
	time.Sleep(FillerSettleTime)
	after := availMB()
	db.DPrintf(db.ALWAYS, "TestMemFillerBasic: after release=%vMB", after)
	assert.True(t, after > during+512,
		"Releasing the filler should have given the memory back: %vMB during, %vMB after", during, after)
}

func TestMemFillerTTLReleasesWithoutEvict(t *testing.T) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return
	}
	defer mrts.Shutdown()
	sc := mrts.GetRealm(REALM1).SigmaClnt

	if swap := mem.GetSwapTotal(); swap > 0 {
		t.Skipf("swap is on (%vMB); the filler refuses to squeeze", swap)
	}

	before := availMB()
	target := before - 1024
	if target < AbsMinFreeMB {
		t.Skipf("only %vMB available; not enough headroom to test a squeeze", before)
	}

	// The TTL is what protects the host when a test dies without evicting, so
	// it is tested the way that happens: nothing here ever sends an evict.
	ttl := 20 * time.Second
	p := spawnFiller(t, sc, target, contentionAbortFloorMB, ttl)
	if p == nil {
		return
	}
	during := availMB()
	assert.True(t, during < before-512, "Filler should have taken memory: %vMB -> %vMB", before, during)

	status, err := sc.WaitExit(p.GetPid())
	if !assert.Nil(t, err, "Err WaitExit: %v", err) {
		return
	}
	assert.True(t, status.IsStatusOK(), "Filler should exit OK on TTL, got %v", status)

	time.Sleep(FillerSettleTime)
	after := availMB()
	db.DPrintf(db.ALWAYS, "TestMemFillerTTL: before=%vMB during=%vMB after=%vMB (ttl %v)", before, during, after, ttl)
	assert.True(t, after > during+512,
		"TTL expiry should have released the memory without any evict: %vMB during, %vMB after", during, after)
}

// TestContentionSanity is the gate before a sweep: every level the sweep will
// use, delivered and withdrawn, with the host checked either side.
//
// It drives startContention rather than spawning fillers directly, so what it
// exercises is the path the benchmarks take, preflight and delivery check
// included.
func TestContentionSanity(t *testing.T) {
	if !contentionEnabled() {
		t.Skip("set -contention_free_mb to the level being checked")
	}
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return
	}
	defer mrts.Shutdown()
	sc := mrts.GetRealm(REALM1).SigmaClnt

	before := availMB()
	ctn := startContention(t, sc)
	during := availMB()
	db.DPrintf(db.ALWAYS, "TestContentionSanity: level=%vMB before=%vMB during=%vMB", contentionFreeMB, before, during)

	assert.InDelta(t, contentionFreeMB, during, ContentionDeliveryTolMB,
		"Level asked to leave %vMB free, %vMB is available", contentionFreeMB, during)
	assert.True(t, during > contentionAbortFloorMB,
		"The squeeze took the host to %vMB, at or under the %vMB abort floor", during, contentionAbortFloorMB)

	// Nothing the kernel needs should have died under the squeeze. A named
	// that cannot be read is the failure this whole safety design exists to
	// prevent, and it is cheap to check.
	_, err = sc.GetDir(sp.NAMED)
	assert.Nil(t, err, "named unreadable under contention, the instance may have been OOM-killed: %v", err)

	ctn.release()
	time.Sleep(FillerSettleTime)
	after := availMB()
	db.DPrintf(db.ALWAYS, "TestContentionSanity: after release=%vMB", after)
	assert.True(t, after > during+512,
		"Releasing should have given the memory back: %vMB during, %vMB after", during, after)
}
