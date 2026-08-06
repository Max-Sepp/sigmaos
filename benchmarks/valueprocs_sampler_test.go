package benchmarks_test

// Sampling of what the value-procs scheduler was doing while a job ran.
//
// Every value-procs benchmark reports its own outcome -- a makespan, a
// quality, a core-second count -- and none of them record the quantity those
// outcomes are supposed to follow from: how many attempts were admitted at
// once, and against what pressure. A final tree dump cannot supply it either,
// since by then nothing is running. So a claim like "the coded quorum shrinks
// from n toward k as the cluster fills" has, until now, been argued from
// makespan alone.
//
// This polls SchedStats for the length of a job and reduces the trace to the
// few numbers those claims are actually about. Polling rather than
// instrumenting the engine keeps this entirely on the benchmark side of the
// RPC: nothing here needs the scheduler rebuilt, and a sampler that falls
// behind loses resolution rather than perturbing the run.

import (
	"fmt"
	"sync"
	"time"

	db "sigmaos/debug"
	"sigmaos/valueprocs/clnt"
)

// VPSampleInterval is how often the sampler polls. Fast enough to catch the
// admission changes in a job measured in seconds, slow enough that the RPCs
// are not themselves a load on what is being measured.
const VPSampleInterval = 100 * time.Millisecond

// vpSample is one observation of the scheduler's state.
type vpSample struct {
	since    time.Duration
	pressure float64
	busy     float64
	memP     float64
	cpuP     float64
	nRunning int
	nCharged int
	slots    int
}

// vpSummary reduces a trace to the numbers a claim is stated in.
//
// Both a max and a mean, because they answer different questions. The max is
// how wide the job ever got, which is what "did it expand into the slack"
// asks. The mean is how wide it was on average, which is what the compute it
// spent follows from.
type vpSummary struct {
	n            int
	maxRunning   int
	meanRunning  float64
	maxCharged   int
	maxPressure  float64
	meanPressure float64
	slots        int
}

func (s vpSummary) String() string {
	return fmt.Sprintf("samples=%d slots=%d running(max=%d mean=%.2f) charged(max=%d) pressure(max=%.3f mean=%.3f)",
		s.n, s.slots, s.maxRunning, s.meanRunning, s.maxCharged, s.maxPressure, s.meanPressure)
}

// vpSampler polls a scheduler for the length of a job.
type vpSampler struct {
	c     *clnt.Clnt
	start time.Time

	mu      sync.Mutex
	samples []vpSample

	stop chan struct{}
	done chan struct{}
}

// startVPSampler begins polling. The caller must stop it.
//
// A failed poll is dropped rather than retried or reported: the scheduler is
// reachable or it is not, the benchmark's own assertions will say so, and a
// sampler that failed loudly would turn a diagnostic into a second source of
// test failures.
func startVPSampler(c *clnt.Clnt) *vpSampler {
	s := &vpSampler{
		c:     c,
		start: time.Now(),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go s.run()
	return s
}

func (s *vpSampler) run() {
	defer close(s.done)
	tick := time.NewTicker(VPSampleInterval)
	defer tick.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-tick.C:
			ss, err := s.c.SchedStats()
			if err != nil {
				continue
			}
			sm := vpSample{
				since:    time.Since(s.start),
				pressure: ss.GetPressure(),
				busy:     ss.GetBusy(),
				nRunning: int(ss.GetNRunning()),
				nCharged: int(ss.GetNCharged()),
				slots:    int(ss.GetSlots()),
			}
			if comp := ss.GetComponents(); comp != nil {
				sm.memP, sm.cpuP = comp["mem"], comp["cpu"]
			}
			s.mu.Lock()
			s.samples = append(s.samples, sm)
			s.mu.Unlock()
		}
	}
}

// summarize stops the sampler and reduces what it collected.
func (s *vpSampler) summarize() vpSummary {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.done

	s.mu.Lock()
	defer s.mu.Unlock()

	out := vpSummary{n: len(s.samples)}
	if out.n == 0 {
		return out
	}
	var sumRunning, sumPressure float64
	for _, sm := range s.samples {
		if sm.nRunning > out.maxRunning {
			out.maxRunning = sm.nRunning
		}
		if sm.nCharged > out.maxCharged {
			out.maxCharged = sm.nCharged
		}
		if sm.pressure > out.maxPressure {
			out.maxPressure = sm.pressure
		}
		if sm.slots > out.slots {
			out.slots = sm.slots
		}
		sumRunning += float64(sm.nRunning)
		sumPressure += sm.pressure
	}
	out.meanRunning = sumRunning / float64(out.n)
	out.meanPressure = sumPressure / float64(out.n)
	return out
}

// report stops the sampler, logs the summary under a caller-supplied label,
// and returns it.
func (s *vpSampler) report(label string) vpSummary {
	sum := s.summarize()
	db.DPrintf(db.ALWAYS, "%v: sched trace %v", label, sum)
	return sum
}

// reportTrace is report plus the full per-sample series, for the arms whose
// claim is about how admission moved over time rather than about where it
// ended up -- the step and pulse contention shapes especially, where the
// interesting number is how long after the squeeze the width changed.
func (s *vpSampler) reportTrace(label string) vpSummary {
	sum := s.summarize()
	db.DPrintf(db.ALWAYS, "%v: sched trace %v", label, sum)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sm := range s.samples {
		db.DPrintf(db.ALWAYS, "%v: sched sample t=%.1fs running=%d charged=%d slots=%d pressure=%.3f busy=%.3f mem=%.3f cpu=%.3f",
			label, sm.since.Seconds(), sm.nRunning, sm.nCharged, sm.slots, sm.pressure, sm.busy, sm.memP, sm.cpuP)
	}
	return sum
}
