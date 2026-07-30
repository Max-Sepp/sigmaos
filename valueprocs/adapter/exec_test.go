package adapter

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"sigmaos/proc"
	"sigmaos/valueprocs"
	"sigmaos/valueprocs/policy"
)

func TestStartReportsStartedThenCompleted(t *testing.T) {
	e, f, s := newExec(t, testPolicy())

	r := ref("r.0", 0)
	e.Start(r, policy.Launch{Workload: template(t, "sleeper")}, policy.StartReason{})

	pid := f.pidOf(t, 0)
	eventually(t, func() bool { return s.count("started") == 1 }, "OnRunStarted")

	f.exit(pid, proc.NewStatusInfo(proc.StatusOK, "done", "payload"), nil)
	eventually(t, func() bool { return len(s.terminals()) == 1 }, "a terminal event")

	term := s.terminals()[0]
	assert.Equal(t, "completed", term.kind)
	assert.Equal(t, r, term.ref)

	// The whole status travels, so the submitter can read Data() exactly as
	// if it had spawned the proc itself.
	st := proc.NewStatusFromBytes(term.data)
	assert.True(t, st.IsStatusOK())
	assert.Equal(t, "payload", st.Data())
}

func TestEachAttemptGetsAFreshPid(t *testing.T) {
	e, f, s := newExec(t, testPolicy())

	tmpl := template(t, "sleeper")
	e.Start(ref("r.0", 0), policy.Launch{Workload: tmpl}, policy.StartReason{})
	first := f.pidOf(t, 0)
	f.exit(first, proc.NewStatus(proc.StatusEvicted), nil)
	eventually(t, func() bool { return len(s.terminals()) == 1 }, "first attempt ended")

	e.Start(ref("r.0", 1), policy.Launch{Workload: tmpl}, policy.StartReason{})
	second := f.pidOf(t, 1)

	// SigmaOS keys a parent's child state on pid and deletes it on exit, so
	// reusing one would make the second attempt unwaitable.
	assert.NotEqual(t, first, second)

	f.mu.Lock()
	var waited []string
	for _, p := range f.waited {
		waited = append(waited, p.String())
	}
	f.mu.Unlock()
	assert.Equal(t, []string{first.String(), second.String()}, waited,
		"each pid is waited on exactly once")
}

func TestLaunchIdentityAndResumeReachTheProc(t *testing.T) {
	e, f, _ := newExec(t, testPolicy())

	r := ref("r.1.0", 7)
	e.Start(r, policy.Launch{Workload: template(t, "sleeper"), Resume: []byte("half")}, policy.StartReason{})

	p := f.procOf(t, 0)
	assert.Equal(t, "t", p.Env[valueprocs.ENV_TREE])
	assert.Equal(t, "r.1.0", p.Env[valueprocs.ENV_NODE])
	assert.Equal(t, "7", p.Env[valueprocs.ENV_RUN])

	got, err := base64.StdEncoding.DecodeString(p.Env[valueprocs.ENV_RESUME])
	assert.NoError(t, err)
	assert.Equal(t, []byte("half"), got)

	// The debug pid must follow the new proc, not the template it was copied
	// from, or every attempt logs under the same name.
	assert.Equal(t, p.GetPid().String(), p.Env[proc.SIGMADEBUGPID])
}

func TestFirstAttemptCarriesNoResume(t *testing.T) {
	e, f, _ := newExec(t, testPolicy())
	e.Start(ref("r.0", 0), policy.Launch{Workload: template(t, "sleeper")}, policy.StartReason{})

	p := f.procOf(t, 0)
	_, ok := p.Env[valueprocs.ENV_RESUME]
	assert.False(t, ok)
}

func TestClassification(t *testing.T) {
	for _, tc := range []struct {
		name    string
		st      *proc.Status
		err     error
		stop    bool
		kind    string
		failure policy.FailureKind
	}{
		{name: "ok", st: proc.NewStatus(proc.StatusOK), kind: "completed"},
		{name: "evicted after a stop", st: proc.NewStatus(proc.StatusEvicted), stop: true, kind: "stopped"},
		{name: "evicted by somebody else", st: proc.NewStatus(proc.StatusEvicted), kind: "failed", failure: policy.FailTransient},
		{name: "fatal", st: proc.NewStatus(proc.StatusFatal), kind: "failed", failure: policy.FailPermanent},
		{name: "error", st: proc.NewStatusErr("boom", nil), kind: "failed", failure: policy.FailTransient},
		{name: "wait errored", err: errPlatform, kind: "failed", failure: policy.FailTransient},
		{name: "no status at all", kind: "failed", failure: policy.FailTransient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pol := testPolicy()
			pol.StopTimeout = time.Hour // the stop must not resolve this
			e, f, s := newExec(t, pol)

			r := ref("r.0", 0)
			e.Start(r, policy.Launch{Workload: template(t, "sleeper")}, policy.StartReason{})
			pid := f.pidOf(t, 0)

			if tc.stop {
				e.Stop(r, policy.StopReason{})
				eventually(t, func() bool { return f.nEvicted() == 1 }, "the eviction was issued")
			}
			f.exit(pid, tc.st, tc.err)

			eventually(t, func() bool { return len(s.terminals()) == 1 }, "a terminal event")
			term := s.terminals()[0]
			assert.Equal(t, tc.kind, term.kind)
			if tc.kind == "failed" {
				assert.Equal(t, tc.failure, term.fail)
			}
		})
	}
}

func TestUnrequestedEvictionIsCounted(t *testing.T) {
	e, f, s := newExec(t, testPolicy())

	e.Start(ref("r.0", 0), policy.Launch{Workload: template(t, "sleeper")}, policy.StartReason{})
	f.exit(f.pidOf(t, 0), proc.NewStatus(proc.StatusEvicted), nil)

	eventually(t, func() bool { return len(s.terminals()) == 1 }, "a terminal event")
	assert.EqualValues(t, 1, e.Stats().NUnrequestedEvicts)
}

func TestSpawnFailureIsTransient(t *testing.T) {
	e, f, s := newExec(t, testPolicy())
	f.spawnErr = errPlatform

	e.Start(ref("r.0", 0), policy.Launch{Workload: template(t, "sleeper")}, policy.StartReason{})

	eventually(t, func() bool { return len(s.terminals()) == 1 }, "a terminal event")
	term := s.terminals()[0]
	assert.Equal(t, "failed", term.kind)
	assert.Equal(t, policy.FailTransient, term.fail)

	// Nothing was placed, so the layer above never hears it started.
	assert.Equal(t, 0, s.count("started"))
	assert.EqualValues(t, 1, e.Stats().NSynthesizedFailed)
	assert.Equal(t, 0, e.Runs())
}

func TestUnrunnableWorkloadIsPermanent(t *testing.T) {
	e, f, s := newExec(t, testPolicy())

	// A workload from somewhere other than this adapter. Retrying cannot help.
	e.Start(ref("r.0", 0), policy.Launch{Workload: otherWorkload{}}, policy.StartReason{})

	eventually(t, func() bool { return len(s.terminals()) == 1 }, "a terminal event")
	assert.Equal(t, policy.FailPermanent, s.terminals()[0].fail)
	assert.Equal(t, 0, f.nSpawned())
}

type otherWorkload struct{}

func (otherWorkload) Name() string { return "other" }

func TestStopRetriesUndeliverableEvictionsThenSynthesizes(t *testing.T) {
	pol := testPolicy()
	pol.StopRetries = 2
	pol.StopTimeout = time.Hour // the retry budget must be what ends this
	e, f, s := newExec(t, pol)

	r := ref("r.0", 0)
	e.Start(r, policy.Launch{Workload: template(t, "sleeper")}, policy.StartReason{})
	f.pidOf(t, 0)

	f.mu.Lock()
	f.evictErr = errPlatform
	f.mu.Unlock()

	e.Stop(r, policy.StopReason{})

	eventually(t, func() bool { return len(s.terminals()) == 1 }, "a synthesized terminal event")
	assert.Equal(t, 3, f.nEvicted(), "the first attempt plus StopRetries more")

	// Reported as a stop, not a failure: the scheduler caps a leaf's
	// failures, and it must not spend that budget on its own decisions.
	term := s.terminals()[0]
	assert.Equal(t, "stopped", term.kind)
	assert.Nil(t, term.data)

	st := e.Stats()
	assert.EqualValues(t, 1, st.NSynthesizedStopped)
	assert.EqualValues(t, 2, st.NEvictRetries)
	assert.EqualValues(t, 0, st.NSynthesizedFailed)
}

func TestStopTimesOutWhenAProcIgnoresIt(t *testing.T) {
	e, f, s := newExec(t, testPolicy())

	r := ref("r.0", 0)
	e.Start(r, policy.Launch{Workload: template(t, "sleeper")}, policy.StartReason{})
	pid := f.pidOf(t, 0)

	// The eviction is delivered. It sets a flag and nothing else; this proc
	// never waits on it, which is the failure mode the whole synthesis path
	// exists for.
	e.Stop(r, policy.StopReason{})

	eventually(t, func() bool { return len(s.terminals()) == 1 }, "a synthesized stop")
	assert.Equal(t, "stopped", s.terminals()[0].kind)
	assert.EqualValues(t, 1, e.Stats().NSynthesizedStopped)

	// The attempt really does exit later. It must not produce a second
	// terminal event.
	f.exit(pid, proc.NewStatus(proc.StatusEvicted), nil)
	consistently(t, 100*time.Millisecond, func() bool { return len(s.terminals()) == 1 },
		"exactly one terminal event per attempt")
}

func TestRealExitBeatsTheSynthesizedOne(t *testing.T) {
	pol := testPolicy()
	pol.StopTimeout = time.Hour
	e, f, s := newExec(t, pol)

	r := ref("r.0", 0)
	e.Start(r, policy.Launch{Workload: template(t, "sleeper")}, policy.StartReason{})
	pid := f.pidOf(t, 0)

	e.Stop(r, policy.StopReason{})
	eventually(t, func() bool { return f.nEvicted() == 1 }, "the eviction was issued")
	f.exit(pid, proc.NewStatusInfo(proc.StatusEvicted, "", "partial"), nil)

	eventually(t, func() bool { return len(s.terminals()) == 1 }, "a terminal event")
	term := s.terminals()[0]
	assert.Equal(t, "stopped", term.kind)

	// The partial progress survives, which is the point of stopping
	// cooperatively rather than synthesizing.
	assert.Equal(t, "partial", proc.NewStatusFromBytes(term.data).Data())
	assert.EqualValues(t, 0, e.Stats().NSynthesizedStopped)
}

func TestStartedNeverFollowsTheTerminalEvent(t *testing.T) {
	pol := testPolicy()
	pol.StopTimeout = time.Millisecond
	e, f, s := newExec(t, pol)

	// The attempt is stopped while the goroutine that would report it started
	// is still parked in WaitStart.
	release := f.holdStart()

	r := ref("r.0", 0)
	e.Start(r, policy.Launch{Workload: template(t, "sleeper")}, policy.StartReason{})
	f.pidOf(t, 0)
	e.Stop(r, policy.StopReason{})

	eventually(t, func() bool { return len(s.terminals()) == 1 }, "a terminal event")
	release()
	consistently(t, 50*time.Millisecond, func() bool { return s.count("started") == 0 },
		"nothing is reported after the terminal event")
}

func TestStopOfAnEndedAttemptIsSilent(t *testing.T) {
	e, f, s := newExec(t, testPolicy())

	r := ref("r.0", 0)
	e.Start(r, policy.Launch{Workload: template(t, "sleeper")}, policy.StartReason{})
	f.exit(f.pidOf(t, 0), proc.NewStatus(proc.StatusOK), nil)
	eventually(t, func() bool { return len(s.terminals()) == 1 }, "a terminal event")

	// The scheduler decided to stop this under its lock, and the attempt
	// finished before the decision reached the platform.
	e.Stop(r, policy.StopReason{})
	consistently(t, 50*time.Millisecond, func() bool { return len(s.terminals()) == 1 },
		"a late stop produces no second terminal event")
	assert.Equal(t, 0, f.nEvicted())
}

func TestTrackingIsDroppedOnTermination(t *testing.T) {
	e, f, s := newExec(t, testPolicy())

	for i := range 3 {
		e.Start(ref("r.0", policy.RunID(i)), policy.Launch{Workload: template(t, "sleeper")}, policy.StartReason{})
		f.pidOf(t, i)
	}
	eventually(t, func() bool { return e.Runs() == 3 }, "three attempts tracked")

	for i := range 3 {
		f.exit(f.pidOf(t, i), proc.NewStatus(proc.StatusOK), nil)
	}
	eventually(t, func() bool { return len(s.terminals()) == 3 }, "all three ended")
	assert.Equal(t, 0, e.Runs())
}

func TestClosedExecStartsNothing(t *testing.T) {
	e, f, s := newExec(t, testPolicy())
	e.Close()

	e.Start(ref("r.0", 0), policy.Launch{Workload: template(t, "sleeper")}, policy.StartReason{})
	eventually(t, func() bool { return len(s.terminals()) == 1 }, "a terminal event even so")
	assert.Equal(t, policy.FailTransient, s.terminals()[0].fail)
	assert.Equal(t, 0, f.nSpawned())

	assert.NotPanics(t, e.Close, "Close is idempotent")
}
