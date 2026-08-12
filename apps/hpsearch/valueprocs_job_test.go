package hpsearch

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// cfg50ms is a config whose only load-bearing field here is IterDur: cost is
// iterations times the CPU an iteration burns, and nothing else in Config
// enters the arithmetic.
func cfg50ms(nConfigs, maxIters int) *Config {
	c := DefaultConfig()
	c.NConfigs, c.MaxIters, c.IterDur = nConfigs, maxIters, 50*time.Millisecond
	return c
}

func finished(configId, iters int) *TrialOutcome {
	return &TrialOutcome{
		ConfigId: configId,
		Finished: true,
		Curve:    &Curve{ConfigId: configId, Scores: make([]float64, iters)},
		Iters:    iters,
		HasIters: true,
	}
}

func stoppedAt(configId, iters int, resident time.Duration) *TrialOutcome {
	return &TrialOutcome{
		ConfigId: configId,
		Iters:    iters,
		HasIters: true,
		Elapsed:  resident,
	}
}

// TestCostIsWorkNotResidency is the regression for the defect the metric
// shipped with: it summed how long each trial was resident, which on an
// oversubscribed machine is several times the CPU the trial actually had.
//
// The numbers are the shape of the real failure. Fifteen trials on four cores:
// one runs to completion, the rest are stopped a third of the way in, and
// every one of them is resident for the whole run because they are all sharing
// the same four cores. Residency sums to more than the baseline spent; work
// sums to less, which is the saving pruning exists to produce.
func TestCostIsWorkNotResidency(t *testing.T) {
	cfg := cfg50ms(15, 300)
	outcomes := []*TrialOutcome{finished(0, 300)}
	for i := 1; i < 15; i++ {
		outcomes = append(outcomes, stoppedAt(i, 100, 40*time.Second))
	}

	baseline := float64(cfg.NConfigs*cfg.MaxIters) * cfg.IterDur.Seconds()
	assert.Equal(t, 225.0, baseline)

	got := MeasuredCoreSeconds(outcomes, nil, cfg)
	assert.Equal(t, (300.0+14*100)*0.05, got)
	assert.Less(t, got, baseline, "pruning eleven of fifteen trials must cost less than running all of them")

	residency := 0.0
	for _, o := range outcomes {
		residency += o.Elapsed.Seconds()
	}
	assert.Greater(t, residency, baseline,
		"residency exceeds the baseline here, which is why it cannot be the cost")
}

// TestCostCountsEveryAttempt pins that a trial stopped and started again is
// charged for both. It restarts its curve from the beginning rather than
// resuming, so the first attempt's work was done, cost the machine, and is
// not represented anywhere in the second attempt's curve.
func TestCostCountsEveryAttempt(t *testing.T) {
	cfg := cfg50ms(2, 300)

	// 120 from an attempt that was stopped, plus 300 from the one that ran to
	// the end -- which is what Wait composes when a finished trial also
	// carries a partial from earlier.
	restarted := finished(0, 300)
	restarted.Iters = 120 + 300

	outcomes := []*TrialOutcome{restarted, stoppedAt(1, 40, time.Second)}
	assert.Equal(t, (420.0+40)*0.05, MeasuredCoreSeconds(outcomes, nil, cfg))
}

// TestUnreportedCostFallsBackToTheCurve covers the trial that was stopped
// without reporting -- it raced the eviction, or died. Its cost is not zero,
// and the reconstruction is what is left; NMeasured is what says so.
func TestUnreportedCostFallsBackToTheCurve(t *testing.T) {
	cfg := cfg50ms(2, 300)
	silent := &TrialOutcome{ConfigId: 1}
	outcomes := []*TrialOutcome{finished(0, 300), silent}
	curves := []*Curve{outcomes[0].Curve, {ConfigId: 1, Scores: make([]float64, 80), Pruned: true}}

	assert.Equal(t, (300.0+80)*0.05, MeasuredCoreSeconds(outcomes, curves, cfg))
	assert.Equal(t, 1, NMeasured(outcomes))

	// And with no curve to fall back on it contributes nothing rather than
	// panicking on a short slice.
	assert.Equal(t, 300.0*0.05, MeasuredCoreSeconds(outcomes, nil, cfg))
}

// TestProgressRoundTrips pins the encoding the resume token travels in, since
// a TrialProgress that decodes to a zero Iters is indistinguishable from a
// trial that never ran and would silently zero that trial's cost.
func TestProgressRoundTrips(t *testing.T) {
	p, err := NewTrialProgress(map[string]any{"ConfigId": 3, "Iters": 117})
	if assert.Nil(t, err, "decode: %v", err) {
		assert.Equal(t, 3, p.ConfigId)
		assert.Equal(t, 117, p.Iters)
	}

	n, ok := progressOf(nil)
	assert.False(t, ok, "nothing to decode is not a report of zero")
	assert.Equal(t, 0, n)

	_, ok = progressOf([]byte("not a marshalled status"))
	assert.False(t, ok, "a token that will not decode is not a report of zero")
}
