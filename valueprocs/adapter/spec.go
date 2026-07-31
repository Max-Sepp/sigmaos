package adapter

import (
	"encoding/base64"
	"fmt"
	"maps"

	"sigmaos/proc"
	sp "sigmaos/sigmap"
	"sigmaos/valueprocs"
	"sigmaos/valueprocs/policy"
	"sigmaos/valueprocs/proto"
)

// ProcTemplate is a leaf's workload: the proc an application wants run, held
// as a pattern rather than as a thing to run.
//
// It cannot be run directly, because SigmaOS keys a parent's child state on
// pid and deletes the entry when the child exits, so a leaf that is stopped
// and later restarted must arrive as a different pid. Build mints one per
// attempt.
type ProcTemplate struct {
	program string
	args    []string
	env     map[string]string
	typ     proc.Ttype
	mcpu    proc.Tmcpu
	mem     proc.Tmem
}

// NewProcTemplate captures p as a leaf's workload.
//
// It rejects the two things about idempotence that are checkable here. A leaf
// may be stopped for capacity and re-run from scratch, so it must not hold a
// reservation the scheduler is unaware of (mcpu) and must be best-effort,
// which is the class of work this layer exists to schedule. Everything else
// about idempotence is a contract with the application.
func NewProcTemplate(p *proc.Proc) (*ProcTemplate, error) {
	if p == nil {
		return nil, fmt.Errorf("valueprocs: nil proc")
	}
	if t := p.GetType(); t != proc.T_BE {
		return nil, fmt.Errorf("valueprocs: leaf %v is %v, must be T_BE", p.GetProgram(), t)
	}
	if m := p.GetMcpu(); m != 0 {
		return nil, fmt.Errorf("valueprocs: leaf %v reserves %v mcpu, must be 0", p.GetProgram(), m)
	}
	return &ProcTemplate{
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
func (t *ProcTemplate) Name() string { return t.program }

// Build mints the proc for one attempt.
//
// The reference and any partial progress go into the environment, which is
// how a running proc learns which attempt it is and how the shim can quote
// that back when it reports a score. The pid is fresh every time.
func (t *ProcTemplate) Build(ref policy.RunRef, l policy.Launch) *proc.Proc {
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

// GroupFromProto builds a schedulable group out of a submitted tree.
//
// This is where a submission stops being a message and becomes work: past
// here nothing is a proto, and the scheduler sees only groups and workloads.
// Every way a client can describe a tree that cannot be run is rejected here
// rather than deeper in, so that a bad submission fails at the caller instead
// of halfway through a job.
func GroupFromProto(n *proto.NodeSpec) (policy.Group, error) {
	if n == nil {
		return nil, fmt.Errorf("valueprocs: nil node")
	}
	var (
		g   policy.Group
		err error
	)
	if len(n.Children) == 0 {
		if n.LeafProc == nil {
			return nil, fmt.Errorf("valueprocs: leaf %q has no proc", n.Label)
		}
		var t *ProcTemplate
		if t, err = NewProcTemplate(proc.NewProcFromProto(n.LeafProc)); err != nil {
			return nil, err
		}
		g, err = policy.Leaf(t)
	} else {
		if n.LeafProc != nil {
			return nil, fmt.Errorf("valueprocs: node %q has both children and a proc", n.Label)
		}
		cs := make([]policy.Group, 0, len(n.Children))
		for _, c := range n.Children {
			var cg policy.Group
			if cg, err = GroupFromProto(c); err != nil {
				return nil, err
			}
			cs = append(cs, cg)
		}
		g, err = policy.Select(int(n.K), cs...)
	}
	if err != nil {
		return nil, err
	}
	if n.Label != "" {
		g = policy.WithLabel(g, n.Label)
	}
	return g, nil
}

// procTemplateFor recovers the ProcTemplate a Launch carries. The policy
// engine never inspects a workload beyond its name, so this is the only place
// the concrete type is needed, and a workload from somewhere else is a
// programming error rather than a runtime condition.
func procTemplateFor(l policy.Launch) (*ProcTemplate, error) {
	t, ok := l.Workload.(*ProcTemplate)
	if !ok {
		return nil, fmt.Errorf("valueprocs: workload %T is not a *ProcTemplate", l.Workload)
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
