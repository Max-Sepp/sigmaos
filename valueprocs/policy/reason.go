package policy

import "fmt"

// StartReasonKind is why an attempt was started.
type StartReasonKind uint8

const (
	StartRequiredForQuorum StartReasonKind = iota + 1 // fewer than k running
	StartSlackRedundancy                              // above k; pressure allows
	StartSpeculativeRacer                             // gradient says a racer helps
	StartRequeuedAfterStop
	StartReplacingFailedRun
)

func (k StartReasonKind) String() string {
	switch k {
	case StartRequiredForQuorum:
		return "required for quorum"
	case StartSlackRedundancy:
		return "slack redundancy"
	case StartSpeculativeRacer:
		return "speculative racer"
	case StartRequeuedAfterStop:
		return "requeued after stop"
	case StartReplacingFailedRun:
		return "replacing failed run"
	}
	return "unknown"
}

// StartReason accompanies every start. Fields are valid per Kind: Gradient
// only for StartSpeculativeRacer, Attempt only for the two retry kinds.
// Pressure is always set.
type StartReason struct {
	Kind     StartReasonKind
	Gradient Gradient
	Pressure float64
	Attempt  int
}

func (r StartReason) String() string {
	s := r.Kind.String()
	switch r.Kind {
	case StartSpeculativeRacer:
		s += fmt.Sprintf(" (gradient %.3f)", float64(r.Gradient))
	case StartRequeuedAfterStop, StartReplacingFailedRun:
		s += fmt.Sprintf(" (attempt %d)", r.Attempt)
	}
	return fmt.Sprintf("%s at pressure %.2f", s, r.Pressure)
}

// StopReasonKind is why an attempt was stopped. The first three end an
// attempt whose goal was reached elsewhere; the last three free its slot for
// something else, and the leaf stays a candidate.
type StopReasonKind uint8

const (
	StopQuorumReached StopReasonKind = iota + 1
	StopRacerLost
	StopTreeUnsatisfiable
	StopTreeCancelled
	StopOutrankedBySibling
	StopCapacityForHigherTree
)

func (k StopReasonKind) String() string {
	switch k {
	case StopQuorumReached:
		return "quorum reached"
	case StopRacerLost:
		return "racer lost"
	case StopTreeUnsatisfiable:
		return "tree unsatisfiable"
	case StopTreeCancelled:
		return "tree cancelled"
	case StopOutrankedBySibling:
		return "outranked by a sibling"
	case StopCapacityForHigherTree:
		return "capacity for a higher tree"
	}
	return "unknown"
}

// StopReason accompanies every stop. Score and BestSiblingScore are a
// snapshot of the ranking that produced the decision and are valid only for
// StopOutrankedBySibling; nothing else preserves them, since the ranking has
// usually moved on by the time anyone asks.
type StopReason struct {
	Kind             StopReasonKind
	Score            Score
	BestSiblingScore Score
	Pressure         float64
}

func (r StopReason) String() string {
	s := r.Kind.String()
	if r.Kind == StopOutrankedBySibling {
		s += fmt.Sprintf(" (score %.3f vs best %.3f)", float64(r.Score), float64(r.BestSiblingScore))
	}
	return fmt.Sprintf("%s at pressure %.2f", s, r.Pressure)
}
