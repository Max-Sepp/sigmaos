// Package burn consumes CPU, for workloads that need to cost something real.
package burn

import "time"

// sink is written by the burn loop and never read, so the compiler cannot
// discard the work as dead.
var sink uint64

// OpsPerMilli is how many burn iterations one unoccupied core completes in a
// millisecond, measured on the development host. Re-measure on a host whose
// cores differ markedly; a wrong value rescales every run without invalidating
// a comparison within one.
//
// Work is fixed rather than bounded by a deadline because a deadline makes a
// task cost the same however much CPU it was given, so shedding nine of ten
// would finish no sooner than keeping them. It is a constant rather than a
// startup calibration so that every task in every arm does identical work.
const OpsPerMilli = 700_000

// For consumes the work one unoccupied core would do in d. d is nominal: a
// task sharing a core with three others takes four times as long.
func For(d time.Duration) {
	Ops(uint64(d.Milliseconds()) * OpsPerMilli)
}

// Ops is For for a caller that would rather state work than a duration. The
// loop is a dependent multiply-add chain, so it is limited by clock rather
// than by issue width and consumes exactly one core.
func Ops(ops uint64) {
	var x uint64 = 1
	for i := uint64(0); i < ops; i++ {
		x = x*6364136223846793005 + 1442695040888963407
	}
	sink += x
}
