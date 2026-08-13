package rooc

import (
	"errors"

	"sigmaos/proc"
	sp "sigmaos/sigmap"
	"sigmaos/util/spstats"
)

// ErrUnknownChild is returned by Evict and WaitExit when the parent holds no
// state for pid, which is what happens once the child has exited: the entry
// is deleted when its exit is collected. It reports that the child has
// already ended rather than that the call failed, so a caller that is trying
// to end the child has nothing left to do and nothing worth retrying.
var ErrUnknownChild = errors.New("proc: unknown child")

type ProcAPI interface {
	// Functions for parent proc
	Spawn(p *proc.Proc) error
	Evict(pid sp.Tpid) error
	WaitStart(pid sp.Tpid) error
	WaitExit(pid sp.Tpid) (*proc.Status, error)

	// Functions for child proc
	Started() error
	Exited(status *proc.Status)
	WaitEvict(pid sp.Tpid) error

	Stats() *spstats.TcounterSnapshot
}
