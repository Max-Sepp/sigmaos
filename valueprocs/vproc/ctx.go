package vproc

import (
	"context"
	"encoding/base64"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	db "sigmaos/debug"
	"sigmaos/proc"
	rpcclntcache "sigmaos/rpc/clnt/cache"
	sprpcclnt "sigmaos/rpc/clnt/sigmap"
	"sigmaos/sigmaclnt"
	"sigmaos/valueprocs"
	"sigmaos/valueprocs/proto"
)

// DefaultScoreInterval is the shortest gap between two score pushes. A tight
// loop may call Score every iteration; only the last value in a window is
// sent.
const DefaultScoreInterval = 100 * time.Millisecond

// Ctx is a value proc's runtime. It reports how the work is going, tells the
// proc when it has been asked to stop, and carries the one exit it is allowed.
type Ctx struct {
	sc     *sigmaclnt.SigmaClnt
	ref    *proto.RunRef // nil when nothing scheduled this proc
	resume []byte

	rpcc     *rpcclntcache.ClntCache
	interval time.Duration
	scores   *coalescer

	graceful  bool
	cancelled atomic.Bool
	done      chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc

	stopOnce sync.Once
	exit     sync.Once
}

type scoreVal struct{ score, gradient float64 }

// Opt configures a Ctx.
type Opt func(*Ctx)

// WithGracefulEvict takes responsibility for stopping.
//
// By default a value proc dies the moment it is evicted, which is safe
// because leaves are required to be idempotent: a half-finished attempt is
// re-run from scratch, so skipping deferred work is the point rather than a
// hazard. Use this only when partial progress is worth reporting, and then
// the proc must watch Cancelled or Done and exit itself.
func WithGracefulEvict() Opt {
	return func(c *Ctx) { c.graceful = true }
}

// WithScoreInterval sets the shortest gap between score pushes.
func WithScoreInterval(d time.Duration) Opt {
	return func(c *Ctx) { c.interval = d }
}

// Start connects, reports the proc started, and arms eviction handling.
func Start(opts ...Opt) (*Ctx, error) {
	sc, err := sigmaclnt.NewSigmaClnt(proc.GetProcEnv())
	if err != nil {
		return nil, err
	}
	return StartWith(sc, opts...)
}

// StartWith is Start for a proc that already has a client.
func StartWith(sc *sigmaclnt.SigmaClnt, opts ...Opt) (*Ctx, error) {
	c := &Ctx{
		sc:       sc,
		interval: DefaultScoreInterval,
		done:     make(chan struct{}),
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	for _, o := range opts {
		o(c)
	}
	c.readIdentity()

	if c.ref != nil {
		c.rpcc = rpcclntcache.NewRPCClntCache(sprpcclnt.WithSPChannel(sc.FsLib, false))
		c.scores = newCoalescer(c.interval, c.push)
	}

	if c.graceful {
		go c.watchEvict()
	} else {
		AutoExitOnEvict(sc)
	}

	if err := sc.Started(); err != nil {
		return nil, err
	}
	return c, nil
}

// readIdentity recovers which attempt this proc is, from the environment the
// scheduler spawned it with.
//
// A proc with no identity is not an error. It is a proc that something other
// than the scheduler started, and it must still run: applications are ported
// onto this shim before their job files are pointed at the scheduler, so the
// same binary has to work spawned either way. Reporting a score then has
// nowhere to go, and goes nowhere.
func (c *Ctx) readIdentity() {
	tree, node := os.Getenv(valueprocs.ENV_TREE), os.Getenv(valueprocs.ENV_NODE)
	if tree == "" || node == "" {
		db.DPrintf(db.VALUEPROC, "vproc: no scheduler identity; scores will be dropped")
		return
	}
	run, err := strconv.ParseUint(os.Getenv(valueprocs.ENV_RUN), 10, 64)
	if err != nil {
		db.DPrintf(db.VALUEPROC_ERR, "vproc: bad run number %q: %v", os.Getenv(valueprocs.ENV_RUN), err)
		return
	}
	c.ref = &proto.RunRef{TID: tree, NodeID: node, Run: run}

	if s := os.Getenv(valueprocs.ENV_RESUME); s != "" {
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			db.DPrintf(db.VALUEPROC_ERR, "vproc: bad resume token: %v", err)
		} else {
			c.resume = b
		}
	}
	db.DPrintf(db.VALUEPROC, "vproc: %v/%v run %v, %v bytes to resume from",
		tree, node, run, len(c.resume))
}

// SigmaClnt returns the underlying client.
func (c *Ctx) SigmaClnt() *sigmaclnt.SigmaClnt { return c.sc }

// NodeID is which leaf of its tree this proc is running, or empty when
// nothing scheduled it. It is for log lines: an application should not need
// to know, since results come back already keyed by it.
func (c *Ctx) NodeID() string {
	if c.ref == nil {
		return ""
	}
	return c.ref.NodeID
}

// --- the value channel -----------------------------------------------------

// Score reports how the work is going.
//
// score is opaque and higher means more deserving of resources; only its
// ordering against the proc's siblings is ever used, so any monotone measure
// will do -- a fraction, an accuracy, a negative loss.
//
// gradient is the marginal value of one more proc racing this one to the same
// goal: near zero when nearly done, large when wedged. It exists because the
// scheduler is forbidden to extrapolate from a score, so a proc that wants to
// be raced when it stalls has to say so.
//
// Calls are coalesced to at most one push per interval, last value winning,
// so calling this every iteration of a tight loop is safe.
func (c *Ctx) Score(score, gradient float64) {
	if c.scores == nil {
		return
	}
	c.scores.offer(scoreVal{score, gradient})
}

func (c *Ctx) push(v scoreVal) {
	req := &proto.ReportScoreReq{Ref: c.ref, Score: v.score, Gradient: v.gradient}
	rep := &proto.ReportScoreRep{}
	// Retried on not-found because the service may be restarting; any other
	// failure is dropped. A score is a hint that repeats, so losing one costs
	// nothing, and blocking the work to deliver it would defeat the purpose.
	if err := c.rpcc.RPCRetryNotFound(valueprocs.VALUESCHED, valueprocs.VALUESCHEDREL,
		"Srv.ReportScore", req, rep); err != nil {
		db.DPrintf(db.VALUEPROC_ERR, "vproc: ReportScore %v: %v", c.ref, err)
	}
}

// --- stopping --------------------------------------------------------------

// watchEvict runs only under WithGracefulEvict, where stopping is the proc's
// own business and this just tells it to.
func (c *Ctx) watchEvict() {
	pid := c.sc.ProcEnv().GetPID()
	if err := c.sc.WaitEvict(pid); err != nil {
		db.DPrintf(db.VALUEPROC_ERR, "vproc: WaitEvict %v: %v", pid, err)
		return
	}
	db.DPrintf(db.VALUEPROC, "vproc: %v evicted, waiting for the proc to stop", pid)
	c.cancelled.Store(true)
	c.stop()
}

// stop releases everything watching for the end. Eviction and exiting can
// both get here, from different goroutines, so it happens once.
func (c *Ctx) stop() {
	c.stopOnce.Do(func() {
		c.cancel()
		close(c.done)
	})
}

// Cancelled reports whether this proc has been asked to stop. It is only ever
// true under WithGracefulEvict; otherwise the proc is already gone.
func (c *Ctx) Cancelled() bool { return c.cancelled.Load() }

// CancelledFlag is Cancelled for code that takes a flag rather than calls a
// method, which is common in inner loops that must not pay for an interface.
func (c *Ctx) CancelledFlag() *atomic.Bool { return &c.cancelled }

// Done closes when this proc has been asked to stop.
func (c *Ctx) Done() <-chan struct{} { return c.done }

// Context is Done for code that takes a context.
func (c *Ctx) Context() context.Context { return c.ctx }

// ResumeToken is what the previous attempt at this leaf reported when it was
// stopped, and is nil on a leaf's first attempt.
func (c *Ctx) ResumeToken() []byte { return c.resume }

// --- exit ------------------------------------------------------------------

// Complete reports the work finished. result travels to whoever submitted the
// tree, since the service is the parent and they cannot collect it otherwise.
func (c *Ctx) Complete(result any) {
	c.finish(proc.NewStatusInfo(proc.StatusOK, "", result))
}

// StoppedWith reports that the proc stopped because it was asked to, handing
// back what it had. Only meaningful under WithGracefulEvict.
func (c *Ctx) StoppedWith(partial any) {
	c.finish(proc.NewStatusInfo(proc.StatusEvicted, "", partial))
}

// Fail reports an attempt that went wrong and is worth trying again.
func (c *Ctx) Fail(err error) {
	c.finish(proc.NewStatusErr(err.Error(), nil))
}

// Fatal reports work that will never succeed, however many times it is run.
// The leaf is finished, and its tree may be too.
func (c *Ctx) Fatal(err error) {
	c.finish(proc.NewStatusInfo(proc.StatusFatal, err.Error(), nil))
}

// finish reports exactly once. Reporting twice is fatal in SigmaOS, and a
// proc with several exit paths is exactly the shape that does it by accident.
func (c *Ctx) finish(st *proc.Status) {
	c.exit.Do(func() {
		c.stop()
		// Drained before the status goes out, so a final score cannot arrive
		// after the scheduler has been told the attempt ended.
		if c.scores != nil {
			c.scores.close()
		}
		db.DPrintf(db.VALUEPROC, "vproc: exiting %v", st)
		if err := c.sc.ClntExit(st); err != nil {
			db.DPrintf(db.VALUEPROC_ERR, "vproc: ClntExit: %v", err)
		}
	})
}
