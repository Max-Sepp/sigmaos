// Package adapter is the SigmaOS half of the value-proc scheduling layer. It
// implements the outbound port the policy engine calls, and it carries the
// inbound transport that feeds the gate.
//
// It exists so that everything platform-shaped is concentrated in one place:
// that eviction is advisory, that a proc cannot be dequeued once queued, that
// a pid may be waited on exactly once, that best-effort admission is
// effectively memory-only. None of that reaches the policy engine, which is
// why the engine is portable and this package is not.
//
// Its one obligation, from which most of the rest follows: every Start and
// every Stop produces exactly one terminal event, eventually, always — even
// when SigmaOS will not make that happen on its own.
package adapter

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	procapi "sigmaos/api/proc"
	db "sigmaos/debug"
	"sigmaos/proc"
	sp "sigmaos/sigmap"
	"sigmaos/valueprocs/policy"
)

// EventSink receives what the platform did. In production it is the gate,
// which funnels each call into the scheduler under its own lock; in tests it
// is a recorder. It is an interface here so that this package does not have
// to import the gate to be testable.
//
// Calls may arrive from any goroutine at any time, and an implementation must
// not call back into Exec while serving one.
type EventSink interface {
	OnRunStarted(ref policy.RunRef)
	OnRunCompleted(ref policy.RunRef, result []byte)
	OnRunStopped(ref policy.RunRef, partial []byte)
	OnRunFailed(ref policy.RunRef, k policy.FailureKind, msg string)
}

// SigmaOSTuning is the adapter's own tuning. None of it is scheduling policy —
// these are all facts about how long SigmaOS takes to do things and how much
// of it there is, which is why they live here rather than in policy.Config,
// and why they would mean nothing on another platform.
type SigmaOSTuning struct {
	// StopRetries is how many times to re-issue an eviction that failed
	// outright, beyond the first attempt.
	StopRetries int

	// StopBackoff is the wait before the second eviction attempt; it doubles
	// each time after that.
	StopBackoff time.Duration

	// StopTimeout bounds the whole stop, from the request to the attempt
	// actually ending. When it expires the terminal event is synthesized, on
	// the grounds that a slot the scheduler can never reclaim is worse than a
	// slot it reclaims optimistically.
	StopTimeout time.Duration

	// ProbePeriod is how often the cluster is measured.
	ProbePeriod time.Duration

	// Oversubscribe is how much concurrency to admit per core.
	//
	// One slot per core is what the machines can honestly run, and that is all
	// Occupancy.Slots ever reports. Anything above one becomes Occupancy.Probe
	// instead: concurrency lent to attempts that have never reported, so a
	// tree with nothing to rank can run every child long enough to learn
	// something and then narrow to what the machines can actually run.
	//
	// So this is an exploration budget rather than a claim about capacity, and
	// raising it widens the opening of a search without leaving it permanently
	// oversubscribed.
	Oversubscribe float64

	// QueueSamples is how many besched shards to ask for queue depth. The
	// answer is diagnostic only, so this is small on purpose.
	QueueSamples int
}

func DefaultSigmaOSTuning() SigmaOSTuning {
	return SigmaOSTuning{
		StopRetries:   3,
		StopBackoff:   500 * time.Millisecond,
		StopTimeout:   60 * time.Second,
		ProbePeriod:   time.Second,
		Oversubscribe: 4.0,
		QueueSamples:  2,
	}
}

var errStopTimedOut = errors.New("stop timed out")

var _ policy.RunStartStopper = (*Exec)(nil)

// Exec implements policy.RunStartStopper over the SigmaOS proc API.
type Exec struct {
	papi   procapi.ProcAPI
	ev     EventSink
	tuning SigmaOSTuning

	mu   sync.Mutex
	runs map[policy.RunRef]*run

	quit chan struct{}
	once sync.Once

	stats stats
}

// run is one attempt's bookkeeping. Two goroutines may touch it: the one that
// owns the proc cradle-to-grave, and a stop goroutine racing it.
type run struct {
	ref policy.RunRef
	pid sp.Tpid

	// stopped records that the scheduler asked for this attempt to end. It
	// decides how an eviction is classified, and getting that wrong breaks an
	// invariant one layer up: a stop must not spend the leaf's failure budget.
	stopped atomic.Bool

	mu      sync.Mutex
	begun   bool
	ended   bool
	endedCh chan struct{}
}

// NewExec returns an Exec that spawns through papi and reports to ev.
func NewExec(papi procapi.ProcAPI, ev EventSink, tuning SigmaOSTuning) *Exec {
	return &Exec{
		papi:   papi,
		ev:     ev,
		tuning: tuning,
		runs:   make(map[policy.RunRef]*run),
		quit:   make(chan struct{}),
	}
}

// Start launches one attempt. It returns as soon as the attempt is
// registered; everything slow happens on the attempt's own goroutine.
func (e *Exec) Start(ref policy.RunRef, l policy.Launch, why policy.StartReason) {
	t, err := procTemplateFor(l)
	if err != nil {
		// Nothing about a retry would change this, so it is permanent: the
		// leaf is unrunnable as submitted.
		db.DPrintf(db.VALUEPROC_ERR, "Start %v: %v", ref, err)
		e.ev.OnRunFailed(ref, policy.FailPermanent, err.Error())
		return
	}
	p := t.Build(ref, l)
	r := &run{ref: ref, pid: p.GetPid(), endedCh: make(chan struct{})}

	// Registered before returning, so a Stop decided in the same reconcile
	// always finds it.
	e.mu.Lock()
	if e.closed() {
		e.mu.Unlock()
		e.ev.OnRunFailed(ref, policy.FailTransient, "adapter closed")
		return
	}
	e.runs[ref] = r
	e.mu.Unlock()

	db.DPrintf(db.VALUEPROC, "Start %v pid %v: %v", ref, r.pid, why)
	go e.exec(r, p)
}

// Stop asks an attempt to end. Returning frees nothing; only the terminal
// event does.
func (e *Exec) Stop(ref policy.RunRef, why policy.StopReason) {
	e.mu.Lock()
	r, ok := e.runs[ref]
	e.mu.Unlock()
	if !ok {
		// The attempt terminated between the decision and this call. Its
		// terminal event has already gone out, so the obligation is met.
		db.DPrintf(db.VALUEPROC, "Stop %v: already ended", ref)
		return
	}
	r.stopped.Store(true)
	db.DPrintf(db.VALUEPROC, "Stop %v pid %v: %v", ref, r.pid, why)
	go e.stop(r)
}

// exec owns one attempt from spawn to terminal event. It is the sole caller
// of WaitExit for its pid, which SigmaOS permits exactly once.
func (e *Exec) exec(r *run, p *proc.Proc) {
	if err := e.papi.Spawn(p); err != nil {
		// Nothing was placed, so there is no exit to wait for and the
		// terminal event has to be manufactured.
		e.stats.synthFailed.Add(1)
		db.DPrintf(db.VALUEPROC_ERR, "Spawn %v: %v", r.ref, err)
		e.end(r, func() {
			e.ev.OnRunFailed(r.ref, policy.FailTransient, "spawn: "+err.Error())
		})
		return
	}

	if err := e.papi.WaitStart(p.GetPid()); err != nil {
		// The attempt may still be running, so this is not terminal — it only
		// means we never saw it start. WaitExit below is what resolves it.
		db.DPrintf(db.VALUEPROC_ERR, "WaitStart %v: %v", r.ref, err)
	} else {
		e.begin(r)
	}

	st, err := e.papi.WaitExit(p.GetPid())
	e.end(r, func() { e.classify(r, st, err) })
}

// classify turns what SigmaOS reports into what the scheduler understands.
//
// The interesting row is an eviction nobody asked for. From the scheduler's
// point of view that is a lost attempt, not one of its own decisions, so it
// must be reported as a failure — reporting it as a stop would hide a real
// problem behind a routine-looking event.
func (e *Exec) classify(r *run, st *proc.Status, err error) {
	switch {
	case err != nil:
		e.stats.synthFailed.Add(1)
		db.DPrintf(db.VALUEPROC_ERR, "WaitExit %v: %v", r.ref, err)
		e.ev.OnRunFailed(r.ref, policy.FailTransient, "waitexit: "+err.Error())

	case st == nil:
		e.stats.synthFailed.Add(1)
		db.DPrintf(db.VALUEPROC_ERR, "WaitExit %v: no status", r.ref)
		e.ev.OnRunFailed(r.ref, policy.FailTransient, "exited without a status")

	case st.IsStatusOK():
		db.DPrintf(db.VALUEPROC, "%v completed", r.ref)
		e.ev.OnRunCompleted(r.ref, result(st))

	case st.IsStatusEvicted() && r.stopped.Load():
		db.DPrintf(db.VALUEPROC, "%v stopped as asked", r.ref)
		e.ev.OnRunStopped(r.ref, result(st))

	case st.IsStatusEvicted():
		e.stats.unrequestedEvicts.Add(1)
		db.DPrintf(db.VALUEPROC_ERR, "%v evicted by another party", r.ref)
		e.ev.OnRunFailed(r.ref, policy.FailTransient, "evicted without a stop request")

	case st.IsStatusFatal():
		db.DPrintf(db.VALUEPROC, "%v failed fatally: %v", r.ref, st.Msg())
		e.ev.OnRunFailed(r.ref, policy.FailPermanent, st.Msg())

	default:
		db.DPrintf(db.VALUEPROC, "%v failed: %v", r.ref, st.Msg())
		e.ev.OnRunFailed(r.ref, policy.FailTransient, st.Msg())
	}
}

// stop delivers an eviction and then bounds how long the attempt may take to
// act on it.
//
// Delivering and ending are separate waits for different reasons. Delivery can
// block indefinitely, because evicting a proc that besched has not placed yet
// waits on the placement. And delivery succeeding proves only that a flag was
// set: a proc that never waits on it runs to completion holding its slot. The
// two share every way of finishing, though, so they are one loop, and which
// wait we are in is just whether sent is still live.
func (e *Exec) stop(r *run) {
	deadline := time.NewTimer(e.tuning.StopTimeout)
	defer deadline.Stop()

	// On its own goroutine because the call itself may block past the
	// deadline.
	sent := make(chan error, 1)
	go func() { sent <- e.evict(r.pid) }()

	for {
		select {
		case <-r.endedCh:
			// It ended, on its own or because it was asked to. Either way the
			// terminal event has already gone out.
			return

		case err := <-sent:
			// Delivery reports itself once. Nil the channel so the same
			// result is not read again, and keep waiting for the end.
			sent = nil
			switch {
			case err == nil:
				// Delivered. Only the proc can end it now.
			case errors.Is(err, procapi.ErrUnknownChild):
				// Not a delivery failure: SigmaOS forgets a child once its
				// exit is collected, so this says the attempt has already
				// ended and WaitExit is on its way with the real event.
				// Synthesizing here would overwrite it.
				db.DPrintf(db.VALUEPROC, "Evict %v pid %v: already exited", r.ref, r.pid)
			default:
				// Undeliverable even after retries, so nothing is going to
				// carry the request to the proc.
				db.DPrintf(db.VALUEPROC_ERR, "Evict %v pid %v: %v", r.ref, r.pid, err)
				e.synthesizeStop(r, err)
				return
			}

		case <-deadline.C:
			e.synthesizeStop(r, errStopTimedOut)
			return

		case <-e.quit:
			// Shutting down; the attempt is abandoned, not reclaimed.
			return
		}
	}
}

// evict issues the eviction, retrying a request that could not be delivered
// at all. It does not retry on the proc failing to die, which is not an error
// SigmaOS reports, nor on the child being unknown, which is permanent.
func (e *Exec) evict(pid sp.Tpid) error {
	backoff := e.tuning.StopBackoff
	var err error
	for i := 0; i <= e.tuning.StopRetries; i++ {
		// If this is a retry, back off before trying again. The first
		// attempt goes immediately.
		if i > 0 {
			e.stats.evictRetries.Add(1)
			t := time.NewTimer(backoff)
			select {
			case <-t.C:
			case <-e.quit:
				t.Stop()
				return err
			}
			t.Stop()
			backoff *= 2
		}

		// Attempt the eviction. A failure falls through to another round,
		// unless the retry budget is spent or the error below says that
		// asking again cannot help.
		if err = e.papi.Evict(pid); err == nil {
			return nil
		}
		if errors.Is(err, procapi.ErrUnknownChild) {
			// The child is gone for good; asking again cannot change that.
			return err
		}
	}
	return err
}

// synthesizeStop reports a stop the platform never confirmed.
//
// It is reported as a stop rather than a failure on purpose. The scheduler
// caps how many times a leaf may fail, and it must not spend that budget on
// the scheduler's own decisions: a leaf stopped for capacity three times
// would otherwise be permanently dead and could never come back when the
// leaders it lost to fail.
//
// The attempt may well still be running, in which case its slot is genuinely
// occupied and the scheduler now believes otherwise. That is visible rather
// than hidden — the machine is still busy, and contention is measured, not
// inferred, so pressure keeps telling the truth.
func (e *Exec) synthesizeStop(r *run, cause error) {
	ended := e.end(r, func() { e.ev.OnRunStopped(r.ref, nil) })
	if !ended {
		return
	}
	e.stats.synthStopped.Add(1)
	db.DPrintf(db.VALUEPROC_ERR,
		"synthesized stop for %v pid %v (%v): the proc did not confirm; it may be bypassing vproc",
		r.ref, r.pid, cause)
}

// begin reports that an attempt is running, at most once and never after its
// terminal event.
func (e *Exec) begin(r *run) {
	r.mu.Lock()
	if r.begun || r.ended {
		r.mu.Unlock()
		return
	}
	r.begun = true
	r.mu.Unlock()
	e.ev.OnRunStarted(r.ref)
}

// end delivers the one terminal event this attempt is allowed, and reports
// whether this call was the one that delivered it. Losers are silent: a
// synthesized stop and a real exit can race, and only the first counts.
func (e *Exec) end(r *run, f func()) bool {
	r.mu.Lock()
	if r.ended {
		r.mu.Unlock()
		return false
	}
	r.ended = true
	r.mu.Unlock()

	e.mu.Lock()
	delete(e.runs, r.ref)
	e.mu.Unlock()

	f()
	close(r.endedCh)
	return true
}

func (e *Exec) closed() bool {
	select {
	case <-e.quit:
		return true
	default:
		return false
	}
}

// Close abandons the attempts still in flight. It does not stop them:
// whether work should outlive the service is the caller's decision, made by
// cancelling first.
//
// It does not wait, and it cannot. A goroutine parked in WaitExit unblocks
// when its proc exits and at no other time, so waiting here would tie the
// service's shutdown to the runtime of the longest job it ever started. Those
// goroutines exit on their own; what Close does is release the ones that are
// merely sleeping between eviction retries, and stop new work from being
// registered.
//
// Attempts abandoned here get no terminal event, which is why this belongs to
// shutdown and nowhere else.
func (e *Exec) Close() {
	e.once.Do(func() { close(e.quit) })
}

// Runs reports how many attempts the adapter is still tracking. It is for
// tests, and for a shutdown that wants to say what it is abandoning.
func (e *Exec) Runs() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.runs)
}

// PidOf returns the proc running an attempt, if it is still in flight.
//
// It exists because a pid is the handle an operator needs to go and look at
// something -- logs, a container, a stack -- and nothing above this package
// knows one. Reporting it is the only reason it leaves here.
func (e *Exec) PidOf(ref policy.RunRef) (sp.Tpid, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.runs[ref]
	if !ok {
		return "", false
	}
	return r.pid, true
}
