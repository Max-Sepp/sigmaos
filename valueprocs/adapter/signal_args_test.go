package adapter

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"sigmaos/ft/procgroupmgr"
	"sigmaos/valueprocs"
	"sigmaos/valueprocs/policy"
)

// TestValueschedArgLayout pins the coupling between how valuesched is spawned
// and how it reads its own arguments.
//
// These two live in different files and neither mentions the other, so an
// off-by-one between them compiles, passes every unit test, and then takes the
// service down at startup -- including for callers that never asked for a
// non-default arm, since the failure is in parsing rather than in the mode.
// Asserting against the argument vector procgroupmgr actually builds is what
// makes that a test failure rather than an "Unreachable" error an hour later.
//
// Both halves of the arm are checked, and against every combination, because
// the sizing argument sits one past the signal and inherits the same hazard.
func TestValueschedArgLayout(t *testing.T) {
	for _, sig := range []policy.Signal{policy.SignalValue, policy.SignalMetrics} {
		for _, sz := range []policy.Sizing{policy.SizingHeadroom, policy.SizingProportional} {
			cfg := procgroupmgr.NewProcGroupConfig(1, "valuesched",
				[]string{sig.String(), sz.String()}, 0, valueprocs.VALUESCHEDREL)
			// os.Args[0] is the binary, which the config does not carry.
			args := append([]string{"valuesched"}, cfg.Args...)

			gotSig, err := signalFromArgs(args)
			if !assert.Nil(t, err, "signal %v should parse out of %v", sig, args) {
				continue
			}
			assert.Equal(t, sig, gotSig, "args %v", args)

			gotSz, err := sizingFromArgs(args)
			if !assert.Nil(t, err, "sizing %v should parse out of %v", sz, args) {
				continue
			}
			assert.Equal(t, sz, gotSz, "args %v", args)
		}
	}
}

// TestSizingFromArgsDefaults covers the vectors that name a signal but no
// sizing, which is every caller written before this axis existed. They must get
// the ledger -- the arm this system proposes -- rather than an error.
func TestSizingFromArgsDefaults(t *testing.T) {
	for _, args := range [][]string{
		{"valuesched"},
		{"valuesched", valueprocs.VALUESCHEDREL},
		{"valuesched", valueprocs.VALUESCHEDREL, "value"},
	} {
		got, err := sizingFromArgs(args)
		assert.Nil(t, err, "args %v", args)
		assert.Equal(t, policy.SizingHeadroom, got, "args %v", args)
	}
}

// TestSizingFromArgsRejectsGarbage keeps the failure loud, for the same reason
// the signal's does: an arm that silently ran the rule it was meant to replace
// would report a comparison between two identical things as a result.
func TestSizingFromArgsRejectsGarbage(t *testing.T) {
	_, err := sizingFromArgs([]string{"valuesched", valueprocs.VALUESCHEDREL, "value", "ratio"})
	assert.NotNil(t, err)
}

// TestSignalFromArgsDefaults covers the vectors that are not a full spawn: a
// service started with no signal at all must run the reporting model rather
// than fail, since that is what every caller before this flag existed passes.
func TestSignalFromArgsDefaults(t *testing.T) {
	for _, args := range [][]string{
		{"valuesched"},
		{"valuesched", valueprocs.VALUESCHEDREL},
	} {
		got, err := signalFromArgs(args)
		assert.Nil(t, err, "args %v", args)
		assert.Equal(t, policy.SignalValue, got, "args %v", args)
	}
}

// TestSignalFromArgsRejectsGarbage keeps the failure loud. A signal that
// cannot be parsed must not quietly become the default: an ablation arm that
// silently ran the model it was meant to ablate would produce a comparison
// between two identical things and report it as a result.
func TestSignalFromArgsRejectsGarbage(t *testing.T) {
	_, err := signalFromArgs([]string{"valuesched", valueprocs.VALUESCHEDREL, "occupancy"})
	assert.NotNil(t, err)
}

// TestTuningOverridesTheDefaults is the point of the whole surface: a sweep
// varies a knob per cell, and a knob that needs a rebuild is a knob nobody
// sweeps.
func TestTuningOverridesTheDefaults(t *testing.T) {
	cfg := DefaultServiceConfig()
	err := applyTuning(&cfg, []string{
		"valuesched", valueprocs.VALUESCHEDREL, "value", "headroom",
		"confirmFor=1500ms", "probePeriod=250ms", "oversubscribe=2.5",
	})
	assert.Nil(t, err)
	assert.Equal(t, 1500*time.Millisecond, cfg.Policy.ConfirmFor)
	assert.Equal(t, 250*time.Millisecond, cfg.SigmaOS.ProbePeriod)
	assert.Equal(t, 2.5, cfg.SigmaOS.Oversubscribe)
}

// TestTuningIsOptionalAndIndependent covers the vectors every existing caller
// passes, which name an arm and nothing else, and the ones that set a single
// knob. Naming one must not disturb the others; a knob nobody mentions keeps
// the default it was built with.
func TestTuningIsOptionalAndIndependent(t *testing.T) {
	base := DefaultServiceConfig()
	for _, args := range [][]string{
		{"valuesched"},
		{"valuesched", valueprocs.VALUESCHEDREL},
		{"valuesched", valueprocs.VALUESCHEDREL, "value"},
		{"valuesched", valueprocs.VALUESCHEDREL, "value", "headroom"},
	} {
		cfg := DefaultServiceConfig()
		assert.Nil(t, applyTuning(&cfg, args), "args %v", args)
		assert.Equal(t, base, cfg, "args %v should change nothing", args)
	}

	cfg := DefaultServiceConfig()
	assert.Nil(t, applyTuning(&cfg, []string{
		"valuesched", valueprocs.VALUESCHEDREL, "value", "headroom", "confirmFor=0",
	}))
	assert.Equal(t, time.Duration(0), cfg.Policy.ConfirmFor,
		"zero is a hold time meaning apply at once, not an unset value")
	assert.Equal(t, base.SigmaOS, cfg.SigmaOS, "the platform knobs are untouched")
}

// TestTuningRejectsGarbage keeps the failure loud, for the same reason the arm
// arguments do. A sweep cell labelled confirmFor=1s that silently ran the
// five-second default would be reported as a measurement of one second.
func TestTuningRejectsGarbage(t *testing.T) {
	for _, bad := range []string{
		"confirmFor",         // not key=value at all
		"confirmfor=1s",      // near miss on the name
		"confirmFor=1",       // a duration needs its unit
		"confirmFor=-1s",     // a hold cannot run backwards
		"probePeriod=0",      // would be a ticker that never ticks
		"oversubscribe=0",    // would leave no concurrency at all
		"oversubscribe=lots", // not a number
	} {
		cfg := DefaultServiceConfig()
		err := applyTuning(&cfg, []string{
			"valuesched", valueprocs.VALUESCHEDREL, "value", "headroom", bad,
		})
		assert.NotNil(t, err, "tuning %q should be rejected", bad)
		if err != nil {
			assert.Contains(t, err.Error(), "valuesched", "tuning %q", bad)
		}
	}
}

// TestKnownTuningIsStable pins that the help text in an error message is
// ordered, so that a failure reads the same way twice.
func TestKnownTuningIsStable(t *testing.T) {
	assert.Equal(t, []string{"confirmFor", "oversubscribe", "probePeriod"},
		knownTuning())
}

// TestTuningEnvReachesTheArgumentVector closes the loop the surface exists for.
// A knob that can be parsed but cannot be set from outside the process is a
// knob no sweep can vary, which is the state this replaces.
func TestTuningEnvReachesTheArgumentVector(t *testing.T) {
	t.Setenv(TuningEnv, "  confirmFor=1s   probePeriod=250ms ")
	args := tuningArgs()
	assert.Equal(t, []string{"confirmFor=1s", "probePeriod=250ms"}, args,
		"whitespace between pairs is separator, not content")

	// And what comes out is what the service will read back in.
	cfg := DefaultServiceConfig()
	full := append([]string{"valuesched", valueprocs.VALUESCHEDREL, "value", "headroom"}, args...)
	assert.Nil(t, applyTuning(&cfg, full))
	assert.Equal(t, time.Second, cfg.Policy.ConfirmFor)
	assert.Equal(t, 250*time.Millisecond, cfg.SigmaOS.ProbePeriod)
}

// TestTuningEnvUnsetAddsNothing keeps the common case free of empty arguments,
// which would shift nothing but would leave every spawn carrying a stray "".
func TestTuningEnvUnsetAddsNothing(t *testing.T) {
	t.Setenv(TuningEnv, "")
	assert.Nil(t, tuningArgs())
}
