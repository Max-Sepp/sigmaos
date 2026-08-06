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
// non-default signal, since the failure is in parsing rather than in the mode.
// Asserting against the argument vector procgroupmgr actually builds is what
// makes that a test failure rather than an "Unreachable" error an hour later.
func TestValueschedArgLayout(t *testing.T) {
	for _, want := range []policy.Signal{policy.SignalValue, policy.SignalMetrics} {
		cfg := procgroupmgr.NewProcGroupConfig(1, "valuesched", []string{want.String()}, 0, valueprocs.VALUESCHEDREL)
		// os.Args[0] is the binary, which the config does not carry.
		args := append([]string{"valuesched"}, cfg.Args...)
		got, err := signalFromArgs(args)
		if !assert.Nil(t, err, "signal %v should parse out of %v", want, args) {
			continue
		}
		assert.Equal(t, want, got, "args %v", args)
	}
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
