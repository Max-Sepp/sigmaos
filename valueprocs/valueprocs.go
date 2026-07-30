// Package valueprocs holds the names shared between the scheduling service
// and the procs it runs.
//
// It is the one thing both halves import, so it must stay free of behaviour:
// leaf types and constants only. The policy engine lives in valueprocs/policy,
// the SigmaOS translation in valueprocs/adapter, and the proc-side runtime in
// valueprocs/vproc.
package valueprocs

// Environment variables carrying an attempt's identity into the proc that
// runs it. The adapter sets them at spawn; the shim reads them once at start
// and quotes the reference back when it reports a score.
//
// They travel in the proc's environment rather than in its arguments because
// a workload's arguments belong to the application, and an attempt number is
// not something an application should have to thread through its own flags.
const (
	// TREE, NODE and RUN together name one attempt. RUN is a decimal integer.
	ENV_TREE = "SIGMAVALUETREE"
	ENV_NODE = "SIGMAVALUENODE"
	ENV_RUN  = "SIGMAVALUERUN"

	// RESUME is the partial progress the previous attempt at this leaf
	// reported when it was stopped, base64-encoded because a proc environment
	// carries strings. It is unset on a leaf's first attempt.
	ENV_RESUME = "SIGMAVALUERESUME"
)
