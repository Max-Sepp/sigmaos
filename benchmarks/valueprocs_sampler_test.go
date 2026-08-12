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
	"math"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	db "sigmaos/debug"
	"sigmaos/valueprocs/clnt"
)

// VPSampleInterval is the shortest gap between two polls. Fast enough to catch
// the admission changes in a job measured in seconds, slow enough that the RPCs
// are not themselves a load on what is being measured.
//
// It is a floor rather than a period. Under load a SchedStats round trip has
// been measured at several hundred milliseconds, so the real cadence is
// whatever the scheduler can answer at, and every sample carries the moment it
// was taken so that an uneven series is still summarised correctly.
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

	// The rest of the slot ledger. Widths alone cannot say why a width was
	// chosen: a search that refuses to narrow looks identical to one that has
	// nothing to narrow for, and telling them apart from outside means
	// reconstructing arithmetic the engine already did. settled is the term
	// sizing decides on and probeHeld is what keeps it from going negative,
	// so a stuck width is read off these two directly.
	probe     int
	probeHeld int
	settled   int64
	nReported int
}

// unbounded is what a ledger figure carries when nothing limits it -- the
// platform has reported no size, or no arbiter divides capacity -- matching
// policy.Unbounded on a 64-bit host. Zero and negative are both meaningful in
// these fields, a settled of -4 being a real contraction of four slots, so
// "no ceiling" needs a value of its own rather than one of theirs.
const unbounded = int64(math.MaxInt64)

// fmtBound keeps the sentinel out of the logs as a twenty-digit number, which
// reads as a measurement rather than as the absence of one.
func fmtBound(v int64) string {
	if v == unbounded {
		return "none"
	}
	return strconv.FormatInt(v, 10)
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

	// The two limbs pressure is the max of. Which one binds decides whether an
	// arm measured what it named: injecting memory contention and then reading
	// a pressure driven by the workers' own cpu burn tests nothing.
	maxMemP float64
	maxCpuP float64
}

// String deliberately omits the limbs. Every value-procs arm logs this line and
// notes/sweep_to_csv.py parses it, so a field added here changes the shape of
// every log in flight. An arm that needs them prints them itself.
func (s vpSummary) String() string {
	return fmt.Sprintf("samples=%d slots=%d running(max=%d mean=%.2f) charged(max=%d) pressure(max=%.3f mean=%.3f)",
		s.n, s.slots, s.maxRunning, s.meanRunning, s.maxCharged, s.maxPressure, s.meanPressure)
}

// vpSampler polls a scheduler for the length of a job.
type vpSampler struct {
	c     clnt.Observer
	start time.Time

	mu      sync.Mutex
	samples []vpSample

	stop chan struct{}
	done chan struct{}
}

// startVPSampler begins polling, timing samples from now. The caller must stop
// it.
//
// A failed poll is dropped rather than retried or reported: the scheduler is
// reachable or it is not, the benchmark's own assertions will say so, and a
// sampler that failed loudly would turn a diagnostic into a second source of
// test failures.
func startVPSampler(c clnt.Observer) *vpSampler {
	return startVPSamplerAt(c, time.Now())
}

// startVPSamplerAt is startVPSampler timing samples from a caller-supplied
// moment, so that a trace can be read against a clock something else owns.
//
// The clock that matters is the contention schedule's: an onset due at +8s and
// a lift at +38s mean nothing against a sampler that started when the job did,
// since the job starts after whatever ran before it. Sharing the epoch is what
// makes "the width changed 2s after the squeeze landed" a statement the trace
// can support.
func startVPSamplerAt(c clnt.Observer, epoch time.Time) *vpSampler {
	s := &vpSampler{
		c:     c,
		start: epoch,
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go s.run()
	return s
}

// run polls back to back, no faster than VPSampleInterval.
//
// The interval is enforced after the round trip rather than by a ticker,
// because a ticker whose period is shorter than the RPC drops ticks silently:
// the poll rate collapses to the RPC rate and the series quietly becomes
// unevenly spaced while still looking periodic. Pacing from the end of each
// call keeps one request in flight, never queues a backlog against a scheduler
// that is already slow, and makes the resulting gaps honest -- which is why
// every sample carries when it was taken and reduce weights by that.
func (s *vpSampler) run() {
	defer close(s.done)
	for {
		select {
		case <-s.stop:
			return
		default:
		}

		began := time.Now()
		if ss, err := s.c.SchedStats(); err == nil {
			sm := vpSample{
				since:     time.Since(s.start),
				pressure:  ss.GetPressure(),
				busy:      ss.GetBusy(),
				nRunning:  int(ss.GetNRunning()),
				nCharged:  int(ss.GetNCharged()),
				slots:     int(ss.GetSlots()),
				probe:     int(ss.GetProbe()),
				probeHeld: int(ss.GetProbeHeld()),
				settled:   ss.GetSettled(),
				nReported: int(ss.GetNChargedReported()),
			}
			if comp := ss.GetComponents(); comp != nil {
				sm.memP, sm.cpuP = comp["mem"], comp["cpu"]
			}
			s.mu.Lock()
			s.samples = append(s.samples, sm)
			s.mu.Unlock()
		}

		wait := VPSampleInterval - time.Since(began)
		if wait <= 0 {
			continue
		}
		t := time.NewTimer(wait)
		select {
		case <-s.stop:
			t.Stop()
			return
		case <-t.C:
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
	return reduce(s.samples)
}

// window reduces only the samples inside [from, to) of the sampler's clock,
// for claims about a change rather than a level: reduced whole, a run that
// expanded and then shed looks exactly like one that never moved.
//
// Call it after summarize or report, or its contents depend on when it was
// asked.
func (s *vpSampler) window(from, to time.Duration) vpSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	var in []vpSample
	for _, sm := range s.samples {
		if sm.since >= from && sm.since < to {
			in = append(in, sm)
		}
	}
	return reduce(in)
}

// reduce summarises a trace.
//
// Means are weighted by how long each sample stood for rather than by sample
// count, because the series is not evenly spaced: polls are paced by what the
// scheduler can answer, and it answers slowest exactly when it is busiest. An
// unweighted mean therefore under-counts the crowded stretches, which are the
// ones every claim here is about.
//
// Leading and trailing samples where the scheduler held nothing are dropped.
// A sampler started on the contention clock runs while some earlier arm is
// still going, and averaging the width of a job over time before it was
// submitted would report a narrower search than ever ran. Only the ends are
// trimmed: a gap in the middle is the job, and belongs in the average.
func reduce(samples []vpSample) vpSummary {
	samples = trimIdle(samples)
	out := vpSummary{n: len(samples)}
	if out.n == 0 {
		return out
	}
	var sumRunning, sumPressure, sumW float64
	for i, sm := range samples {
		if sm.nRunning > out.maxRunning {
			out.maxRunning = sm.nRunning
		}
		if sm.nCharged > out.maxCharged {
			out.maxCharged = sm.nCharged
		}
		if sm.pressure > out.maxPressure {
			out.maxPressure = sm.pressure
		}
		if sm.memP > out.maxMemP {
			out.maxMemP = sm.memP
		}
		if sm.cpuP > out.maxCpuP {
			out.maxCpuP = sm.cpuP
		}
		if sm.slots > out.slots {
			out.slots = sm.slots
		}
		// A sample stands for the stretch up to the next one. The last stands
		// for as long as the one before it did, there being nothing after it to
		// measure against.
		w := VPSampleInterval.Seconds()
		switch {
		case i+1 < len(samples):
			w = (samples[i+1].since - sm.since).Seconds()
		case i > 0:
			w = (sm.since - samples[i-1].since).Seconds()
		}
		if w <= 0 {
			w = VPSampleInterval.Seconds()
		}
		sumRunning += float64(sm.nRunning) * w
		sumPressure += sm.pressure * w
		sumW += w
	}
	out.meanRunning = sumRunning / sumW
	out.meanPressure = sumPressure / sumW
	return out
}

// trimIdle drops the runs of samples at each end where the scheduler held
// nothing. A trace that is idle throughout is left alone, since there is
// nothing to centre on and a summary of no samples explains less than a
// summary of idle ones.
func trimIdle(samples []vpSample) []vpSample {
	lo, hi := 0, len(samples)
	for lo < hi && samples[lo].nCharged == 0 {
		lo++
	}
	for hi > lo && samples[hi-1].nCharged == 0 {
		hi--
	}
	if lo == hi {
		return samples
	}
	return samples[lo:hi]
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
		// The ledger goes at the end so that the existing prefix still parses:
		// notes/sweep_to_csv.py's pattern for this line is unanchored, and every
		// log already collected keeps its meaning.
		db.DPrintf(db.ALWAYS, "%v: sched sample t=%.1fs running=%d charged=%d slots=%d pressure=%.3f busy=%.3f mem=%.3f cpu=%.3f probe=%d held=%d settled=%v reported=%d",
			label, sm.since.Seconds(), sm.nRunning, sm.nCharged, sm.slots, sm.pressure, sm.busy, sm.memP, sm.cpuP,
			sm.probe, sm.probeHeld, fmtBound(sm.settled), sm.nReported)
	}
	return sum
}

// --- reduce -----------------------------------------------------------------

func vpAt(sinceMs int, running, charged int, pressure float64) vpSample {
	return vpSample{
		since:    time.Duration(sinceMs) * time.Millisecond,
		nRunning: running, nCharged: charged, pressure: pressure, slots: 4,
	}
}

// TestReduceWeightsByTimeNotBySampleCount is the bias the old summary carried.
//
// Polls are paced by what the scheduler can answer and it answers slowest when
// it is busiest, so the crowded stretches are exactly the ones with fewest
// samples in them. Counting samples reports the quiet stretch the run spent
// least time in.
func TestReduceWeightsByTimeNotBySampleCount(t *testing.T) {
	// Width 12 for four seconds, sampled once because the scheduler was
	// labouring, then width 2 for one second sampled every 100ms.
	got := []vpSample{vpAt(0, 12, 12, 1.0)}
	for ms := 4000; ms <= 5000; ms += 100 {
		got = append(got, vpAt(ms, 2, 2, 0.0))
	}
	r := reduce(got)

	assert.InDelta(t, 10.0, r.meanRunning, 0.5,
		"four of the five seconds were spent at width 12")
	assert.InDelta(t, 0.8, r.meanPressure, 0.05)
	assert.Equal(t, 12, r.maxRunning)
}

// TestReduceIgnoresIdleEnds covers a sampler started on the contention clock,
// which runs while some earlier arm is still going. Averaging a job's width
// over time before it was submitted reports a narrower search than ever ran.
func TestReduceIgnoresIdleEnds(t *testing.T) {
	var got []vpSample
	for ms := 0; ms < 3000; ms += 100 {
		got = append(got, vpAt(ms, 0, 0, 0.2)) // the baseline arm, not this one
	}
	for ms := 3000; ms < 4000; ms += 100 {
		got = append(got, vpAt(ms, 8, 8, 0.9))
	}
	for ms := 4000; ms < 6000; ms += 100 {
		got = append(got, vpAt(ms, 0, 0, 0.1)) // and after it finished
	}
	r := reduce(got)

	assert.InDelta(t, 8.0, r.meanRunning, 0.01, "only the stretch it was running counts")
	assert.Equal(t, 10, r.n)
}

// TestReduceKeepsAnIdleTraceWhole is the corner: a job that never ran should
// report as idle rather than as no samples at all, since the second explains
// less than the first.
func TestReduceKeepsAnIdleTraceWhole(t *testing.T) {
	var got []vpSample
	for ms := 0; ms < 500; ms += 100 {
		got = append(got, vpAt(ms, 0, 0, 0.3))
	}
	r := reduce(got)
	assert.Equal(t, 5, r.n)
	assert.Equal(t, 0.0, r.meanRunning)
}
