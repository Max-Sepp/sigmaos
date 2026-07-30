package adapter

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"sigmaos/proc"
	"sigmaos/valueprocs"
	"sigmaos/valueprocs/policy"
)

func TestTemplateRejectsWhatCannotBeRerun(t *testing.T) {
	// A leaf may be stopped for capacity and re-run from scratch, so it must
	// be best-effort work that holds no reservation of its own.
	lc := proc.NewProc("sleeper", nil)
	lc.SetType(proc.T_LC)
	_, err := NewTemplate(lc)
	assert.Error(t, err)

	reserved := proc.NewProc("sleeper", nil)
	reserved.SetMcpu(1000)
	_, err = NewTemplate(reserved)
	assert.Error(t, err)

	_, err = NewTemplate(nil)
	assert.Error(t, err)

	ok, err := NewTemplate(proc.NewProc("sleeper", []string{"a"}))
	assert.NoError(t, err)
	assert.Equal(t, "sleeper", ok.Name())
}

func TestTemplateOutlivesTheProcItCaptured(t *testing.T) {
	p := proc.NewProc("sleeper", []string{"a"})
	p.AppendEnv("K", "v")
	tm, err := NewTemplate(p)
	assert.NoError(t, err)

	// The submitter still holds its proc and may do anything with it.
	p.AppendEnv("K", "changed")

	built := tm.Build(policy.RunRef{Tree: "t", Node: "r", Run: 0}, policy.Launch{})
	assert.Equal(t, "v", built.Env["K"])
}

func TestBuildCarriesResourcesFromTheTemplate(t *testing.T) {
	p := proc.NewProc("sleeper", nil)
	p.SetMem(256)
	tm, err := NewTemplate(p)
	assert.NoError(t, err)

	built := tm.Build(policy.RunRef{Tree: "t", Node: "r", Run: 3}, policy.Launch{})
	assert.EqualValues(t, 256, built.GetMem())
	assert.Equal(t, proc.T_BE, built.GetType())
	assert.EqualValues(t, 0, built.GetMcpu())
	assert.Equal(t, "3", built.Env[valueprocs.ENV_RUN])
}

func TestResultRehydrates(t *testing.T) {
	assert.Nil(t, result(nil))

	b := result(proc.NewStatusInfo(proc.StatusOK, "msg", map[string]any{"n": 1.0}))
	st := proc.NewStatusFromBytes(b)
	assert.True(t, st.IsStatusOK())
	assert.Equal(t, "msg", st.Msg())
	assert.Equal(t, map[string]any{"n": 1.0}, st.Data())
}
