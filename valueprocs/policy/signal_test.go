package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// metricsConfig is testConfig with the signal ablated, so every test below
// differs from its value-model counterpart in exactly one field.
func metricsConfig() Config {
	c := testConfig()
	c.Signal = SignalMetrics
	return c
}

// TestMetricsSignalIgnoresAWedgedAttempt is the ablation's defining property,
// stated against the value model's defining one.
//
// TestRaceOnlyWhatIsNotProgressing/wedged_is_raced sets up an incumbent that
// has stopped converting time into value and shows that a fresh attempt
// overtakes it. Under a metrics signal the identical state produces nothing:
// occupancy cannot distinguish a wedged attempt from a healthy one, so there
// is no evidence for the comparison to weigh and the slot stays where it is.
//
// This is what makes the metrics arm a fair baseline rather than a broken one.
// It is not that the engine has been disabled; it is that the decision this
// engine exists to make cannot be made from the numbers this mode can see.
func TestMetricsSignalIgnoresAWedgedAttempt(t *testing.T) {
	// The same pressure the value-model test uses, so slack alone holds the
	// node at k and only a value comparison could move it.
	const p = 0.52

	s, f := newSched(metricsConfig())
	busy(s, t0, p)
	submit(t, s, t0, "t", selG(t, 1, leafG(t, "a"), leafG(t, "b")))
	startQueued(s, f, t0)

	f.reset()
	// Half done and converting no more time into value: under SignalValue this
	// is exactly the report that starts a racer.
	apply(s.OnScore(t0, refOf("t", "r.0", 0), 0.5, 0.0))

	assert.Empty(t, f.starts(), "a metrics signal has nothing to race on")
	assert.Equal(t, 1, nodeView(t, s, "t", "r").Target,
		"target should stay at k, since only pressure can size it")
}

// TestMetricsSignalStillSizesOnPressure pins what the ablation keeps. It is
// not a scheduler that does nothing: the count it runs still follows the
// cluster's occupancy, which is precisely the policy the introduction
// describes as the state of the art.
func TestMetricsSignalStillSizesOnPressure(t *testing.T) {
	s, f := newSched(metricsConfig())
	sized(s, t0, 4)
	// An empty cluster, so slack pays for every child.
	submit(t, s, t0, "t", selG(t, 1, leavesG(t, 3)...))
	assert.Equal(t, 3, nodeView(t, s, "t", "r").Target,
		"at zero pressure every child is slack-funded")
	assert.Len(t, f.starts(), 3)

	// Fill the cluster and the same node contracts to its quorum, on nothing
	// but the occupancy reading.
	busy(s, t0, 1.0)
	assert.Equal(t, 1, nodeView(t, s, "t", "r").Target,
		"at full pressure a node keeps only its quorum")
}

// TestMetricsSignalShedsOldestLast checks the surviving order. With every
// incumbent worth the same, rank falls through to age, which is the "keep what
// has been running longest" rule a threshold-driven descheduler follows.
func TestMetricsSignalShedsOldestLast(t *testing.T) {
	s, f := newSched(metricsConfig())
	sized(s, t0, 4)
	submit(t, s, t0, "t", selG(t, 1, leavesG(t, 3)...))
	startQueued(s, f, t0)

	// Reports that would reorder the ranking under the value model. The last
	// child is made to look far and away the best.
	apply(s.OnScore(t0, refOf("t", "r.0", 0), 0.1, 0.0))
	apply(s.OnScore(t0, refOf("t", "r.2", 0), 0.9, 1.0))

	f.reset()
	busy(s, t0, 1.0)

	// Two of the three go, and which two is decided by age rather than by
	// anything reported -- so the survivor is the one that started first, not
	// the one that claimed the most.
	if assert.Len(t, f.stops(), 2) {
		kept := map[NodeID]bool{"r.0": true, "r.1": true, "r.2": true}
		for _, c := range f.stops() {
			delete(kept, c.ref.Node)
		}
		assert.Contains(t, kept, NodeID("r.0"),
			"the oldest attempt survives, whatever the others reported")
	}
}

// TestSignalRoundTrips guards the spelling the deployment flag depends on: a
// mode selected by name has to come back as the mode that was asked for.
func TestSignalRoundTrips(t *testing.T) {
	for _, want := range []Signal{SignalValue, SignalMetrics} {
		got, err := ParseSignal(want.String())
		assert.Nil(t, err)
		assert.Equal(t, want, got)
	}
	if _, err := ParseSignal("occupancy"); !assert.NotNil(t, err) {
		return
	}
	// An empty name is the default rather than an error, so a deployment that
	// passes no argument at all gets the reporting model.
	got, err := ParseSignal("")
	assert.Nil(t, err)
	assert.Equal(t, SignalValue, got)
}
