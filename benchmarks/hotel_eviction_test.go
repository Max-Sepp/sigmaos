package benchmarks_test

import (
	"fmt"
	"math/rand"
	"time"

	"sigmaos/benchmarks"
	db "sigmaos/debug"
	"sigmaos/proc"
	mschedclnt "sigmaos/sched/msched/clnt"
	sp "sigmaos/sigmap"
)

// selectAndEvictReplica picks one running instance of cfg.Svc in ji's realm
// according to cfg.Policy (random / oldest-by-spawn-time / newest-by-spawn-time)
// and evicts it via the existing, unmodified procclnt.Evict primitive. Returns
// the evicted pid and the time at which the evict was issued, for correlating
// against the load generator's request timeline.
func selectAndEvictReplica(ji *HotelJobInstance, cfg *benchmarks.EvictionBenchConfig) (sp.Tpid, time.Time, error) {
	msc := mschedclnt.NewMSchedClnt(ji.SigmaClnt.FsLib, sp.NOT_SET)
	running, err := msc.GetAllRunningProcs()
	if err != nil {
		return "", time.Time{}, err
	}

	candidates := make([]*proc.Proc, 0)
	for _, p := range running[ji.GetRealm()] {
		if p.GetProgram() == cfg.Svc {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		return "", time.Time{}, fmt.Errorf("no running %v replicas found in realm %v", cfg.Svc, ji.GetRealm())
	}

	var target *proc.Proc
	switch cfg.Policy {
	case benchmarks.EvictRandom:
		target = candidates[rand.Intn(len(candidates))]
	case benchmarks.EvictOldest:
		target = candidates[0]
		for _, p := range candidates[1:] {
			if p.GetSpawnTime().Before(target.GetSpawnTime()) {
				target = p
			}
		}
	case benchmarks.EvictNewest:
		target = candidates[0]
		for _, p := range candidates[1:] {
			if p.GetSpawnTime().After(target.GetSpawnTime()) {
				target = p
			}
		}
	default:
		return "", time.Time{}, fmt.Errorf("unknown eviction policy %v", cfg.Policy)
	}

	db.DPrintf(db.ALWAYS, "Eviction (%v policy): evicting %v[%v] spawned at %v (of %v candidates)",
		cfg.Policy, cfg.Svc, target.GetPid(), target.GetSpawnTime(), len(candidates))
	evictedAt := time.Now()
	err = ji.Evict(target.GetPid())
	return target.GetPid(), evictedAt, err
}
