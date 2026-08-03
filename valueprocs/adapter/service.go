package adapter

import (
	"hash/fnv"

	db "sigmaos/debug"
	"sigmaos/proc"
	"sigmaos/sigmaclnt"
	"sigmaos/sigmasrv"
	"sigmaos/util/crash"
	"sigmaos/valueprocs"
	"sigmaos/valueprocs/gate"
	"sigmaos/valueprocs/policy"
)

// Config is everything the service is tuned by. The two halves are kept apart
// on purpose: Policy is how the scheduler decides, SigmaOS is how long the
// platform takes to do things, and only the first would mean anything on
// another platform.
type Config struct {
	Policy  policy.Config
	SigmaOS SigmaOSTuning
}

func DefaultServiceConfig() Config {
	return Config{Policy: policy.DefaultConfig(), SigmaOS: DefaultSigmaOSTuning()}
}

// NewSrv assembles the layer.
//
// The wiring is the whole architecture in six lines, and the direction of
// each arrow matters. The scheduler calls the executor and knows nothing
// else. The executor reports to the gate, which is the only thing permitted
// to touch the scheduler. The RPC surface calls the gate too. Nothing calls
// back into the scheduler while it is deciding, which is what lets it be
// single-threaded and therefore testable.
func NewSrv(sc *sigmaclnt.SigmaClnt, cfg Config) *Srv {
	var (
		ex   *Exec
		g    *gate.Gate
		sd   *policy.Scheduler
		logf = func(f string, v ...any) { db.DPrintf(db.VALUEPROC, f, v...) }
	)
	// Exec needs the gate to report to and the gate needs a scheduler that
	// needs Exec, so one of the three is filled in after the fact. It is Exec,
	// because it is the only one whose reference is not used until an attempt
	// actually starts.
	ex = NewExec(sc, nil, cfg.SigmaOS)
	sd = policy.NewScheduler(cfg.Policy, ex, FairShare{}, logf)
	g = gate.New(sd, gate.WithEpoch(epochOf(sc.ProcEnv())))
	ex.ev = g

	return &Srv{
		gate:      g,
		exec:      ex,
		probe:     NewProbe(sc, cfg.SigmaOS.Oversubscribe, cfg.SigmaOS.QueueSamples),
		period:    cfg.SigmaOS.ProbePeriod,
		submitted: make(map[policy.TreeID]string),
	}
}

// epochOf identifies this incarnation of the service's state.
//
// It is regenerated, never updated: there is no counter to increment and
// nothing to persist, because "differs from last time" is a far weaker
// requirement than "greater than last time", and it is free.
//
// procgroupmgr counts restarts and passes the count in, which orders
// incarnations as well as distinguishing them. Where there is no manager
// there is no count, and the pid is enough, since it only has to differ --
// SigmaOS mints a fresh one for every proc.
//
// Never zero. A reader that has not read yet sends zero, so zero has to mean
// "asserting nothing" and nothing else; were the service's own epoch zero,
// every position would be accepted and the check would quietly do nothing.
func epochOf(pe *proc.ProcEnv) uint64 {
	if g := proc.GetSigmaGen(); g >= 0 {
		return uint64(g) + 1
	}
	h := fnv.New64a()
	h.Write([]byte(pe.GetPID().String()))
	return h.Sum64() | 1
}

// Run starts the service and blocks until it is told to stop.
func (s *Srv) Run(sc *sigmaclnt.SigmaClnt) error {
	// The client is handed over rather than left to be created here. A proc
	// gets exactly one: the dial proxy is initialised per process, and a
	// second client aborts the process on the spot.
	ssrv, err := sigmasrv.NewSigmaSrvClnt(valueprocs.VALUESCHED, sc, s)
	if err != nil {
		return err
	}

	// The service is supervised and holds every tree in memory, so losing it
	// is a scenario rather than a hypothetical: a restart cannot collect the
	// exits of procs the previous incarnation spawned, and every client's
	// position in its results becomes meaningless. The epoch is how they find
	// that out, and these are what let a test make it happen on purpose.
	crash.Failer(sc.FsLib, crash.VALUESCHED_CRASH, func(e crash.Tevent) {
		crash.Crash()
	})
	crash.Failer(sc.FsLib, crash.VALUESCHED_PARTITION, func(e crash.Tevent) {
		db.DPrintf(db.VALUEPROC, "valuesched partitioned, delay %v", e.Delay)
		crash.PartitionAll(sc.FsLib)
	})

	s.gate.Run()
	// The probe pushes; it is not consulted on the scheduling tick. A machine
	// that answers slowly therefore makes the occupancy reading late rather
	// than making every decision late.
	s.probe.Run(s.period, s.gate.OnOccupancy)
	db.DPrintf(db.VALUEPROC, "valuesched serving at %v, epoch %v",
		valueprocs.VALUESCHED, s.gate.Epoch())

	// This is where the service sits for its whole life. Despite the name,
	// RunServer is not a serve loop: NewSigmaSrvClnt started the listener
	// above, and every call is dispatched on its own goroutine -- which is
	// what the gate exists to serialize. RunServer marks the proc started
	// and then parks in WaitEvict on its own pid, so reaching the next line
	// means this proc has been evicted and the service is shutting down.
	err = ssrv.RunServer()
	s.probe.Close()

	// Stop accepting work and let the attempts still in flight go. Whether
	// they should outlive the service is not this layer's call: an
	// application's trees are its own, and a service that killed them on
	// every restart would make restarting worse than staying down.
	s.gate.Close()
	s.exec.Close()
	db.DPrintf(db.VALUEPROC, "valuesched exiting, %v attempts abandoned", s.exec.Runs())
	return err
}

// RunSrv is the whole of the valuesched proc.
func RunSrv() error {
	sc, err := sigmaclnt.NewSigmaClnt(proc.GetProcEnv())
	if err != nil {
		return err
	}
	return NewSrv(sc, DefaultServiceConfig()).Run(sc)
}
