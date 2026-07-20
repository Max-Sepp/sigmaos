package benchmarks_test

import (
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"sigmaos/apps/hotel"
	"sigmaos/benchmarks"
	db "sigmaos/debug"
	sp "sigmaos/sigmap"
	"sigmaos/test"
)

const (
	// EvictionNCache is the number of cache replicas the job runs with, so
	// that the three eviction policies actually have something to
	// differentiate -- with a single replica, every policy evicts the same
	// (and only) one.
	EvictionNCache = 3
	// EvictionDelay is how long into the load run we wait before evicting a
	// replica.
	EvictionDelay = 15 * time.Second
	// EvictionDur is the total duration of the load run; must leave enough
	// time after EvictionDelay to observe recovery.
	EvictionDur    = 30 * time.Second
	EvictionMaxRPS = 100
)

// evictionSample is one load-generator request's outcome, timestamped so it
// can be bucketed relative to when the eviction was issued.
type evictionSample struct {
	t       time.Time
	success bool
}

// newEvictionHotelBenchConfig starts from the shared, already-wired
// HotelBenchConfig (see config_test.go's TestMain) and overrides just what
// this benchmark needs: more cache replicas (so the eviction policies can
// differ), the load shape, and the eviction config itself.
func newEvictionHotelBenchConfig(policy benchmarks.EvictionPolicy) *benchmarks.HotelBenchConfig {
	cfg := *HotelBenchConfig
	jobCfg := *cfg.JobCfg
	cacheCfg := *jobCfg.CacheCfg
	cacheCfg.NSrv = EvictionNCache
	jobCfg.CacheCfg = &cacheCfg
	cfg.JobCfg = &jobCfg
	cfg.Durs = []time.Duration{EvictionDur}
	cfg.MaxRPS = []int{EvictionMaxRPS}
	cfg.EvictBenchCfg = benchmarks.NewEvictionBenchConfig("cached", true, EvictionDelay, policy)
	return &cfg
}

// runHotelEvictionJob runs a hotel search load against cfg, evicting one
// "cached" replica (per cfg.EvictBenchCfg.Policy) partway through. It returns
// every request's (timestamp, success) sample and the time the evict was
// issued.
func runHotelEvictionJob(t *testing.T, ts1 *test.RealmTstate, cfg *benchmarks.HotelBenchConfig) ([]evictionSample, time.Time) {
	var mu sync.Mutex
	samples := make([]evictionSample, 0)

	fn := func(wc *hotel.WebClnt, r *rand.Rand) {
		err := hotel.RandSearchReq(wc, r)
		mu.Lock()
		samples = append(samples, evictionSample{t: time.Now(), success: err == nil})
		mu.Unlock()
	}

	pc := newRealmCostPerf(ts1)
	defer pc.Done()
	dc := NewDeploymentCost(pc)
	p := newRealmPerf(ts1)
	defer p.Done()

	ji := NewHotelJob(ts1, p, dc, true, fn, false, cfg)

	var evictedAt time.Time
	var evictErr error
	var evictWg sync.WaitGroup
	evictWg.Go(func() {
		time.Sleep(cfg.EvictBenchCfg.EvictDelay)
		_, evictedAt, evictErr = selectAndEvictReplica(ji, cfg.EvictBenchCfg)
	})

	ji.StartHotelJob()
	ji.Wait()
	evictWg.Wait()
	assert.Nil(t, evictErr, "Error evicting replica: %v", evictErr)

	return samples, evictedAt
}

// summarizeEvictionSamples buckets samples into 1s windows relative to
// evictedAt and prints the failure rate per bucket, so the
// spike-then-recover shape (or its absence) is directly visible in the test
// log, alongside a simple recovery-time estimate (first bucket whose failure
// rate returns to at or below the pre-eviction baseline).
func summarizeEvictionSamples(policy benchmarks.EvictionPolicy, samples []evictionSample, evictedAt time.Time) {
	const bucket = 1 * time.Second
	type counts struct{ total, failed int }
	buckets := make(map[int]counts)
	var pre counts
	for _, s := range samples {
		off := s.t.Sub(evictedAt)
		if off < 0 {
			pre.total++
			if !s.success {
				pre.failed++
			}
			continue
		}
		idx := int(off / bucket)
		c := buckets[idx]
		c.total++
		if !s.success {
			c.failed++
		}
		buckets[idx] = c
	}
	preFailRate := 0.0
	if pre.total > 0 {
		preFailRate = float64(pre.failed) / float64(pre.total)
	}
	db.DPrintf(db.ALWAYS, "Eviction (%v policy): pre-eviction requests=%v failRate=%.3f", policy, pre.total, preFailRate)
	recovered := -1
	for i := range int(EvictionDur / bucket) {
		c := buckets[i]
		failRate := 0.0
		if c.total > 0 {
			failRate = float64(c.failed) / float64(c.total)
		}
		db.DPrintf(db.ALWAYS, "Eviction (%v policy): t+%vs requests=%v failed=%v failRate=%.3f", policy, i, c.total, c.failed, failRate)
		if recovered == -1 && c.total > 0 && failRate <= preFailRate {
			recovered = i
		}
	}
	if recovered >= 0 {
		db.DPrintf(db.ALWAYS, "Eviction (%v policy): recovered to pre-eviction failure rate by t+%vs", policy, recovered)
	} else {
		db.DPrintf(db.ALWAYS, "Eviction (%v policy): did not recover to pre-eviction failure rate within %v", policy, EvictionDur)
	}
}

func testHotelEviction(t *testing.T, policy benchmarks.EvictionPolicy) {
	mrts, err := test.NewMultiRealmTstate(t, []sp.Trealm{REALM1})
	if !assert.Nil(t, err, "Error New Tstate: %v", err) {
		return
	}
	defer mrts.Shutdown()

	// The extra cache replicas (EvictionNCache) need more scheduling capacity
	// than a single local node offers; mirrors TestHotelDevSigmaosSearchScaleCache's
	// use of BootMinNode for the same reason.
	N := 3
	if !assert.Nil(t, mrts.GetRoot().BootMinNode(N), "Boot node") {
		return
	}

	cfg := newEvictionHotelBenchConfig(policy)
	samples, evictedAt := runHotelEvictionJob(t, mrts.GetRealm(REALM1), cfg)
	if !assert.False(t, evictedAt.IsZero(), "Eviction never happened") {
		return
	}
	summarizeEvictionSamples(policy, samples, evictedAt)
}

// TestHotelEvictionRandom, TestHotelEvictionOldest, and TestHotelEvictionNewest
// each run the same load against a 3-replica cache and evict one replica
// mid-run per a different naive policy, since no real eviction policy exists
// in apps/hotel/apps/cache today -- establishing the "blast radius" of a
// value-blind eviction choice as a baseline for a future value-aware policy
// to improve on.
func TestHotelEvictionRandom(t *testing.T) {
	testHotelEviction(t, benchmarks.EvictRandom)
}

func TestHotelEvictionOldest(t *testing.T) {
	testHotelEviction(t, benchmarks.EvictOldest)
}

func TestHotelEvictionNewest(t *testing.T) {
	testHotelEviction(t, benchmarks.EvictNewest)
}
