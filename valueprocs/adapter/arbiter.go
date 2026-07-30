package adapter

import "sigmaos/valueprocs/policy"

// FairShare divides capacity equally between the trees competing for it.
//
// It is the default, and for now the only, arbiter. Cross-tree ordering is
// meant to end up as sysadmin configuration keyed on the attributes a tree
// carries — tenant, principal, program, label — and not as something an
// application declares about itself, since an application asked to rank its
// own importance always answers "high". Equal shares is the policy that
// presumes least while that configuration does not exist yet.
//
// It is deliberately not proportional to what a tree asked for. A tree that
// submits a hundred leaves is not more deserving than one that submits three;
// it just has more slack, which is the scheduler's to spend.
type FairShare struct {
	// Floor is the smallest share a tree may be given, so that a large number
	// of trees cannot divide capacity down to nothing and deadlock every one
	// of them below its quorum. Zero means one slot.
	Floor int
}

// Arbitrate implements policy.Arbiter. Trees are in submission order, and any
// remainder goes to the oldest, so that a tie is broken in favour of work
// that has been waiting longest.
func (f FairShare) Arbitrate(trees []policy.TreeView, c policy.Capacity) []policy.TreeShare {
	if len(trees) == 0 {
		return nil
	}
	// No stated capacity means no basis to divide anything; returning nothing
	// leaves every tree unlimited, which is the right reading of "unknown".
	if c.Slots <= 0 {
		return nil
	}

	floor := f.Floor
	if floor < 1 {
		floor = 1
	}

	share := c.Slots / len(trees)
	extra := c.Slots % len(trees)

	out := make([]policy.TreeShare, 0, len(trees))
	for i, t := range trees {
		n := share
		if i < extra {
			n++
		}
		if n < floor {
			n = floor
		}
		out = append(out, policy.TreeShare{Tree: t.ID, Slots: n})
	}
	return out
}
