package adapter

import (
	"testing"

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
