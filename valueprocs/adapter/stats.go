package adapter

import "sync/atomic"

// stats counts what the platform did that the scheduler could not see.
type stats struct {
	synthStopped      atomic.Int64
	synthFailed       atomic.Int64
	evictRetries      atomic.Int64
	unrequestedEvicts atomic.Int64
}

// Stats is the adapter's contribution to explaining the layer's behaviour.
// The scheduler's own counters say what it decided; these say how well the
// platform carried the decisions out.
type Stats struct {
	// NSynthesizedStopped counts attempts the adapter declared stopped
	// without the platform confirming it. A nonzero value is a bug report
	// about a proc — almost always one that bypassed the shim and so never
	// waits for its eviction — rather than normal operation.
	NSynthesizedStopped int64

	// NSynthesizedFailed counts attempts the adapter lost track of: a spawn
	// that never took, a wait that errored, a machine that went away.
	NSynthesizedFailed int64

	// NEvictRetries counts re-issued evictions. A rising count without a
	// rising NSynthesizedStopped means evictions are slow but landing.
	NEvictRetries int64

	// NUnrequestedEvicts counts attempts evicted by somebody other than this
	// layer. It should be zero; if it is not, something else is competing for
	// the same procs.
	NUnrequestedEvicts int64
}

// Stats returns a snapshot.
func (e *Exec) Stats() Stats {
	return Stats{
		NSynthesizedStopped: e.stats.synthStopped.Load(),
		NSynthesizedFailed:  e.stats.synthFailed.Load(),
		NEvictRetries:       e.stats.evictRetries.Load(),
		NUnrequestedEvicts:  e.stats.unrequestedEvicts.Load(),
	}
}
