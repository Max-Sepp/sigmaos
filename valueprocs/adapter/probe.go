package adapter

import (
	"sync"
	"time"

	db "sigmaos/debug"
	beschedclnt "sigmaos/sched/besched/clnt"
	mschedclnt "sigmaos/sched/msched/clnt"
	mschedproto "sigmaos/sched/msched/proto"
	"sigmaos/sigmaclnt"
	sp "sigmaos/sigmap"
	"sigmaos/valueprocs/policy"
)

// loadSource and queueSource are the slices of the scheduler clients the
// probe uses. They are interfaces only so that the fold below -- which is the
// part with judgment in it -- can be tested against a fleet described in a
// table rather than one that has to be booted.
type loadSource interface {
	MSchedLoad() (map[string]*mschedproto.GetMSchedLoadRep, error)
}

type queueSource interface {
	GetQueueStats(nsample int) (map[sp.Trealm]int, error)
}

// Probe measures how contended the cluster is and pushes the answer in.
//
// It pushes rather than being polled, for two reasons. Failure gets an
// obvious home: log it, keep the last good reading, skip the update -- where
// a puller would have to be handed an error it has no basis to answer. And it
// decouples the RPC fan-out from the scheduling tick, so one slow machine
// jitters the occupancy reading instead of delaying every decision behind it.
type Probe struct {
	loads   loadSource
	queues  queueSource
	realm   sp.Trealm
	over    float64
	nsample int

	mu   sync.Mutex
	last policy.Occupancy
	seen bool

	quit chan struct{}
	once sync.Once
}

// NewProbe returns a probe reading the schedulers this client can see.
//
// It needs no kernel privilege: the read RPCs enforce no realm check, and the
// root scheduler directories are mounted into every realm's namespace, so an
// ordinary proc can measure the machines its work lands on.
func NewProbe(sc *sigmaclnt.SigmaClnt, over float64, nsample int) *Probe {
	return &Probe{
		loads:   mschedclnt.NewMSchedClnt(sc.FsLib, sp.NOT_SET),
		queues:  beschedclnt.NewBESchedClnt(sc.FsLib),
		realm:   sc.ProcEnv().GetRealm(),
		over:    over,
		nsample: nsample,
		quit:    make(chan struct{}),
	}
}

// Run measures on a timer until Close. It returns immediately.
func (p *Probe) Run(period time.Duration, sink func(policy.Occupancy)) {
	go func() {
		t := time.NewTicker(period)
		defer t.Stop()
		for {
			select {
			case <-p.quit:
				return
			case <-t.C:
				if o, ok := p.Sample(); ok {
					sink(o)
				}
			}
		}
	}()
}

// Sample takes one measurement. It reports false when there was nothing to
// measure, which is not the same as measuring zero: a cluster nobody can see
// is not an idle cluster, and reporting it as one would have the scheduler
// spend slack that does not exist.
func (p *Probe) Sample() (policy.Occupancy, bool) {
	loads, err := p.loads.MSchedLoad()
	if err != nil || len(loads) == 0 {
		db.DPrintf(db.VALUEPROC_ERR, "Probe: MSchedLoad err %v, %v machines", err, len(loads))
		return p.lastGood()
	}

	o, ok := fold(loads, p.over)
	if !ok {
		return p.lastGood()
	}

	// Queue depth is carried for a human to look at and for nothing else. It
	// is the one number here that is not sound as a quantity: GetQueueStats
	// picks random shards, its dedup loop skips a collision rather than
	// retrying it, and it sums the raw depths without normalizing by how many
	// distinct shards it actually reached. So the magnitude means nothing,
	// which is why realm/srv only ever tests it for being above zero. The
	// scheduler measures the same phenomenon directly and exactly, by how
	// long its own attempts sit queued, so nothing needs this.
	if q, err := p.queues.GetQueueStats(p.nsample); err == nil {
		o.Components["beQueuedSampled"] = float64(q[p.realm])
	}

	p.mu.Lock()
	p.last, p.seen = o, true
	p.mu.Unlock()
	return o, true
}

func (p *Probe) lastGood() (policy.Occupancy, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last, p.seen
}

// Close stops the timer.
func (p *Probe) Close() { p.once.Do(func() { close(p.quit) }) }

// fold reduces a fleet to one number.
//
// The two terms are combined with a max rather than a weighted sum, and that
// is the whole design of this function.
//
// Memory alone cannot see the workload this layer exists to schedule.
// Best-effort admission gates on free memory, and best-effort procs routinely
// reserve none -- codedmatmul's workers default to zero, documented as
// leaving them "unconstrained by besched's memory-based admission". So a
// fleet pinning every core reports all of its memory free. That is not a
// hypothetical: a freshly booted kernel under test measures 15807MB of
// 15807MB free at 50% CPU. Memory says idle; the machine is half gone.
//
// A max says the cluster is full when any one signal saturates, which is the
// right reading when each signal is blind to a different way of being full.
//
// The memory term prefers memAvailMB, what the kernel says can still be
// allocated, over memFreeMB, the admission ledger. The ledger only moves when
// a proc declares a reservation, so a machine filled by procs that declare
// nothing reads as empty. The ledger is the fallback for a peer that does not
// sample availability, which memAvailValid distinguishes from a machine
// whose real availability is genuinely zero.
func fold(loads map[string]*mschedproto.GetMSchedLoadRep, over float64) (policy.Occupancy, bool) {
	var (
		memFree, memTotal float64
		cpuWeighted       float64
		cores             int64
	)
	for _, l := range loads {
		if l.GetMemAvailValid() {
			memFree += float64(l.GetMemAvailMB())
		} else {
			memFree += float64(l.GetMemFreeMB())
		}
		memTotal += float64(l.GetMemTotalMB())
		// Weighted by cores, so a 32-core machine at 90% counts for more than
		// a 2-core machine at 90%.
		cpuWeighted += float64(l.GetCpuUtil()) * float64(l.GetNCores())
		cores += int64(l.GetNCores())
	}
	if cores <= 0 {
		return policy.Occupancy{}, false
	}

	var memP float64
	if memTotal > 0 {
		memP = 1 - memFree/memTotal
	}
	cpuP := cpuWeighted / (100 * float64(cores))

	if over <= 0 {
		over = 1
	}
	return policy.Occupancy{
		Busy:  saturate(max(memP, cpuP)),
		Slots: int(float64(cores) * over),
		Components: map[string]float64{
			"mem":      saturate(memP),
			"cpu":      saturate(cpuP),
			"machines": float64(len(loads)),
			"cores":    float64(cores),
		},
	}, true
}

func saturate(x float64) float64 { return min(max(x, 0), 1) }
