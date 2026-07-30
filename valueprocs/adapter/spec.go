package adapter

import (
	"encoding/base64"
	"fmt"
	"maps"

	"sigmaos/proc"
	sp "sigmaos/sigmap"
	"sigmaos/valueprocs"
	"sigmaos/valueprocs/policy"
)

// Template is a leaf's workload: the proc an application wants run, held as a
// pattern rather than as a thing to run.
//
// It cannot be run directly, because SigmaOS keys a parent's child state on
// pid and deletes the entry when the child exits, so a leaf that is stopped
// and later restarted must arrive as a different pid. Build mints one per
// attempt.
type Template struct {
	program string
	args    []string
	env     map[string]string
	typ     proc.Ttype
	mcpu    proc.Tmcpu
	mem     proc.Tmem
}

// NewTemplate captures p as a leaf's workload.
//
// It rejects the two things about idempotence that are checkable here. A leaf
// may be stopped for capacity and re-run from scratch, so it must not hold a
// reservation the scheduler is unaware of (mcpu) and must be best-effort,
// which is the class of work this layer exists to schedule. Everything else
// about idempotence is a contract with the application.
func NewTemplate(p *proc.Proc) (*Template, error) {
	if p == nil {
		return nil, fmt.Errorf("valueprocs: nil proc")
	}
	if t := p.GetType(); t != proc.T_BE {
		return nil, fmt.Errorf("valueprocs: leaf %v is %v, must be T_BE", p.GetProgram(), t)
	}
	if m := p.GetMcpu(); m != 0 {
		return nil, fmt.Errorf("valueprocs: leaf %v reserves %v mcpu, must be 0", p.GetProgram(), m)
	}
	return &Template{
		program: p.GetProgram(),
		args:    p.Args,
		env:     maps.Clone(p.Env),
		typ:     p.GetType(),
		mcpu:    p.GetMcpu(),
		mem:     p.GetMem(),
	}, nil
}

// Name satisfies policy.Workload. It is the program, since that is what makes
// a log line about a leaf legible.
func (t *Template) Name() string { return t.program }

// Build mints the proc for one attempt.
//
// The reference and any partial progress go into the environment, which is
// how a running proc learns which attempt it is and how the shim can quote
// that back when it reports a score. The pid is fresh every time.
func (t *Template) Build(ref policy.RunRef, l policy.Launch) *proc.Proc {
	p := proc.NewProcPid(sp.GenPid(t.program), t.program, t.args)

	// The template's environment first, then the identity, so a stale
	// reference copied from a template cannot survive into a new attempt.
	for k, v := range t.env {
		p.AppendEnv(k, v)
	}
	// NewProcPid seeded this from the pid it was given; the copy above may
	// have overwritten it with the template's.
	p.AppendEnv(proc.SIGMADEBUGPID, p.GetPid().String())

	p.AppendEnv(valueprocs.ENV_TREE, string(ref.Tree))
	p.AppendEnv(valueprocs.ENV_NODE, string(ref.Node))
	p.AppendEnv(valueprocs.ENV_RUN, fmt.Sprintf("%d", ref.Run))
	if len(l.Resume) > 0 {
		p.AppendEnv(valueprocs.ENV_RESUME, base64.StdEncoding.EncodeToString(l.Resume))
	}

	p.SetType(t.typ)
	p.SetMcpu(t.mcpu)
	p.SetMem(t.mem)
	return p
}

// templateFor recovers the Template a Launch carries. The policy engine never
// inspects a workload beyond its name, so this is the only place the concrete
// type is needed, and a workload from somewhere else is a programming error
// rather than a runtime condition.
func templateFor(l policy.Launch) (*Template, error) {
	t, ok := l.Workload.(*Template)
	if !ok {
		return nil, fmt.Errorf("valueprocs: workload %T is not a *Template", l.Workload)
	}
	return t, nil
}

// result encodes a finished attempt for the client that submitted it.
//
// The whole proc.Status travels, not just its payload, so the submitter can
// rehydrate it with proc.NewStatusFromBytes and read Data() exactly as it
// would have if it had spawned the proc itself. That matters because the
// service is the parent (it spawns), so the submitter never sees the status
// any other way.
func result(st *proc.Status) []byte {
	if st == nil {
		return nil
	}
	return st.Marshal()
}
