package vproc

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"sigmaos/valueprocs"
)

// recorder collects what a coalescer actually sent.
type recorder struct {
	mu   sync.Mutex
	sent []scoreVal

	// hold parks every send, and entered reports that one has been reached,
	// so a test can be sure a send is genuinely in flight rather than merely
	// likely to be.
	hold      chan struct{}
	entered   chan struct{}
	enterOnce sync.Once
}

func (r *recorder) send(v scoreVal) {
	if r.hold != nil {
		r.enterOnce.Do(func() { close(r.entered) })
		<-r.hold
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, v)
}

func (r *recorder) all() []scoreVal {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]scoreVal(nil), r.sent...)
}

func (r *recorder) n() int { return len(r.all()) }

func eventually(t *testing.T, f func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting: %s", msg)
}

func TestFirstScoreIsNotDelayed(t *testing.T) {
	r := &recorder{}
	c := newCoalescer(time.Hour, r.send)
	defer c.close()

	// A proc that reports once and then computes for a minute must not look
	// silent for the first interval of it.
	c.offer(scoreVal{0.1, 0})
	eventually(t, func() bool { return r.n() == 1 }, "the first score went straight out")
	assert.Equal(t, scoreVal{0.1, 0}, r.all()[0])
}

func TestOnlyTheNewestScoreInAWindowSurvives(t *testing.T) {
	r := &recorder{}
	c := newCoalescer(40*time.Millisecond, r.send)
	defer c.close()

	// A tight inner loop. A score describes a state, not an event, so the
	// older values are worthless the moment the newer ones exist.
	for i := range 500 {
		c.offer(scoreVal{float64(i), 0})
	}

	// How many sends this becomes is not a property -- whether the pump wakes
	// during the loop or after it is a scheduling accident. What is
	// guaranteed is that the newest value is never the one dropped: a token
	// is only discarded when one is already pending, and a pending token
	// means the pump will wake again and read whatever is latest by then.
	eventually(t, func() bool {
		s := r.all()
		return len(s) > 0 && s[len(s)-1].score == 499
	}, "the last value offered arrives")

	assert.Less(t, r.n(), 10, "500 offers did not become 500 sends")
}

func TestScoresAreRateLimited(t *testing.T) {
	r := &recorder{}
	c := newCoalescer(50*time.Millisecond, r.send)
	defer c.close()

	start := time.Now()
	offers := 0
	for time.Since(start) < 120*time.Millisecond {
		c.offer(scoreVal{1, 0})
		offers++
		time.Sleep(time.Millisecond)
	}
	elapsed := time.Since(start)

	// The ceiling is the property: one send per interval, plus the immediate
	// first. A lower bound would only be asserting that this machine ran the
	// pump promptly, which is not something the coalescer promises.
	assert.LessOrEqual(t, r.n(), int(elapsed/(50*time.Millisecond))+1)
	assert.Greater(t, offers, 10*r.n(), "offers vastly outnumber sends")
}

func TestCloseDrainsASendInFlight(t *testing.T) {
	r := &recorder{hold: make(chan struct{}), entered: make(chan struct{})}
	c := newCoalescer(time.Millisecond, r.send)

	c.offer(scoreVal{1, 0})
	// Wait for the send to actually start. Racing close against offer is a
	// different question -- select picks either when both are ready, and a
	// dropped score is fine because a score repeats.
	<-r.entered

	closed := make(chan struct{})
	go func() { c.close(); close(closed) }()

	// close must not return while a push is still on the wire, or a score
	// could reach the scheduler after the attempt has been reported ended.
	select {
	case <-closed:
		t.Fatal("close returned with a send outstanding")
	case <-time.After(30 * time.Millisecond):
	}

	close(r.hold)
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("close did not drain")
	}

	assert.NotPanics(t, c.close, "close is idempotent")
}

func TestOfferAfterCloseIsHarmless(t *testing.T) {
	r := &recorder{}
	c := newCoalescer(time.Millisecond, r.send)
	c.close()

	n := r.n()
	assert.NotPanics(t, func() {
		for range 10 {
			c.offer(scoreVal{1, 0})
		}
	})
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, n, r.n(), "nothing is sent after close")
}

func TestIdentityIsReadFromTheEnvironment(t *testing.T) {
	t.Setenv(valueprocs.ENV_TREE, "job-7")
	t.Setenv(valueprocs.ENV_NODE, "r.1.0")
	t.Setenv(valueprocs.ENV_RUN, "3")
	t.Setenv(valueprocs.ENV_RESUME, "aGFsZg==") // "half"

	c := &Ctx{}
	c.readIdentity()

	if assert.NotNil(t, c.ref) {
		assert.Equal(t, "job-7", c.ref.TID)
		assert.Equal(t, "r.1.0", c.ref.NodeID)
		assert.EqualValues(t, 3, c.ref.Run)
	}
	assert.Equal(t, []byte("half"), c.ResumeToken())
}

// TestAProcNobodyScheduledStillRuns is what lets an application be ported
// onto this shim before its job file is pointed at the scheduler: the same
// binary has to work spawned either way, so a missing identity is an ordinary
// condition rather than an error.
func TestAProcNobodyScheduledStillRuns(t *testing.T) {
	c := &Ctx{}
	c.readIdentity()

	assert.Nil(t, c.ref)
	assert.Nil(t, c.ResumeToken())
	assert.NotPanics(t, func() { c.Score(0.5, 0.1) }, "reporting a score goes nowhere")
}

func TestABadIdentityIsIgnoredRatherThanFatal(t *testing.T) {
	t.Setenv(valueprocs.ENV_TREE, "job-7")
	t.Setenv(valueprocs.ENV_NODE, "r.0")
	t.Setenv(valueprocs.ENV_RUN, "not-a-number")

	c := &Ctx{}
	c.readIdentity()
	assert.Nil(t, c.ref, "the proc runs; it just cannot be identified")
}

func TestFirstAttemptHasNoResumeToken(t *testing.T) {
	t.Setenv(valueprocs.ENV_TREE, "job-7")
	t.Setenv(valueprocs.ENV_NODE, "r.0")
	t.Setenv(valueprocs.ENV_RUN, "0")

	c := &Ctx{}
	c.readIdentity()
	assert.NotNil(t, c.ref)
	assert.Nil(t, c.ResumeToken())
}
