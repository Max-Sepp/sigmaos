package adapter

import (
	"fmt"
	"hash/fnv"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

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
// RunSrv boots the service.
//
// The two arguments it takes name the arm: the policy signal (see
// policy.Signal), "value" or "metrics", and the sizing rule (see policy.Sizing),
// "headroom" or "proportional". They are arguments rather than build tags so a
// single binary can serve every arm of a comparison, which is what makes them
// runnable in one sweep against one deployment.
func RunSrv() error {
	sc, err := sigmaclnt.NewSigmaClnt(proc.GetProcEnv())
	if err != nil {
		return err
	}
	cfg := DefaultServiceConfig()
	sig, err := signalFromArgs(os.Args)
	if err != nil {
		return err
	}
	sz, err := sizingFromArgs(os.Args)
	if err != nil {
		return err
	}
	cfg.Policy.Signal, cfg.Policy.Sizing = sig, sz
	if err := applyTuning(&cfg, os.Args); err != nil {
		return err
	}
	db.DPrintf(db.ALWAYS, "valuesched: policy signal %v sizing %v, confirmFor %v probePeriod %v oversubscribe %v",
		cfg.Policy.Signal, cfg.Policy.Sizing, cfg.Policy.ConfirmFor,
		cfg.SigmaOS.ProbePeriod, cfg.SigmaOS.Oversubscribe)
	return NewSrv(sc, cfg).Run(sc)
}

// argTuning is where the optional overrides begin, one past the sizing rule.
//
// They are key=value rather than further positions because position is what
// argSignal and argSizing already cost us once: a vector is built in one file
// and read in another, neither mentions the other, and an off-by-one compiles
// and passes every unit test before taking the service down at startup. A name
// cannot be off by one, and a caller may pass one knob without passing the
// three it does not care about.
const argTuning = 4

// tuning is what a deployment may set without a rebuild.
//
// These are the numbers an experiment varies, and every one of them was chosen
// for "a cluster of long-running batch work" while the jobs measured here run
// for seconds. Requiring a full build per value made the obvious experiment --
// sweep the hold time, look at the settle time -- cost more than the result was
// worth, which is why it had not been done.
var tuning = map[string]func(*Config, string) error{
	"confirmFor": func(c *Config, v string) error {
		d, err := nonNegativeDuration(v)
		// Zero is meaningful: apply a proposal the moment it is made.
		c.Policy.ConfirmFor = d
		return err
	},
	"probePeriod": func(c *Config, v string) error {
		d, err := nonNegativeDuration(v)
		if err == nil && d <= 0 {
			return fmt.Errorf("must be positive, since it drives a ticker")
		}
		c.SigmaOS.ProbePeriod = d
		return err
	},
	"oversubscribe": func(c *Config, v string) error {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("want a number: %v", err)
		}
		if f <= 0 {
			return fmt.Errorf("must be positive; one slot per core is %q", "1")
		}
		c.SigmaOS.Oversubscribe = f
		return nil
	},
}

func nonNegativeDuration(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("want a duration such as 1s or 500ms: %v", err)
	}
	if d < 0 {
		return 0, fmt.Errorf("must not be negative")
	}
	return d, nil
}

// applyTuning reads the optional overrides off the end of the argument vector.
//
// An unrecognised key is an error rather than a shrug, for the same reason a
// bad signal is: a sweep that quietly ran the default under a label naming
// something else would report a comparison between two identical things as a
// result. A vector with no overrides at all is the common case and does
// nothing.
func applyTuning(cfg *Config, args []string) error {
	if len(args) <= argTuning {
		return nil
	}
	for _, a := range args[argTuning:] {
		k, v, ok := strings.Cut(a, "=")
		if !ok {
			return fmt.Errorf("valuesched: tuning %q: want key=value, one of %v",
				a, knownTuning())
		}
		set, ok := tuning[k]
		if !ok {
			return fmt.Errorf("valuesched: unknown tuning %q: want one of %v",
				k, knownTuning())
		}
		if err := set(cfg, v); err != nil {
			return fmt.Errorf("valuesched: tuning %v: %v", k, err)
		}
	}
	return nil
}

// knownTuning is the key set, sorted, so an error message reads the same way
// twice and a test can assert on it.
func knownTuning() []string {
	out := make([]string, 0, len(tuning))
	for k := range tuning {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// argSignal and argSizing are where the arm sits in valuesched's argument
// vector.
//
// They start at index 2 rather than 1 because procgroupmgr prepends the job name
// to whatever StartJobArm passes (ft/procgroupmgr/procgroupmgr.go's
// NewProcGroupConfigRealmSwitch), so the layout is:
//
//	os.Args = [binary, job, signal, sizing, key=value...]
//
// Reading index 1 instead parses the job name as a signal, which fails, which
// takes the service down before it serves anything -- and takes down the
// default arm too, not just the ablation. TestValueschedArgLayout pins this.
const (
	argSignal = 2
	argSizing = 3
)

// signalFromArgs reads the policy signal out of valuesched's argument vector,
// defaulting to the reporting model when none was passed.
func signalFromArgs(args []string) (policy.Signal, error) {
	if len(args) <= argSignal {
		return policy.SignalValue, nil
	}
	return policy.ParseSignal(args[argSignal])
}

// sizingFromArgs reads the sizing rule out of valuesched's argument vector.
//
// A missing argument is the ledger rather than an error, so a deployment that
// predates this axis -- or a caller that only names a signal -- gets the arm
// this system proposes rather than the one it argues against.
func sizingFromArgs(args []string) (policy.Sizing, error) {
	if len(args) <= argSizing {
		return policy.SizingHeadroom, nil
	}
	return policy.ParseSizing(args[argSizing])
}
