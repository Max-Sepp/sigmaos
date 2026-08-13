package policy

import "fmt"

// StartReasonKind is why an attempt was started.
type StartReasonKind uint8

const (
	StartRequiredForQuorum StartReasonKind = iota + 1 // fewer than k running
	StartSlackRedundancy                              // above k; a slot was free
	StartDerivedValue                                 // above k; outbid an incumbent
	StartRequeuedAfterStop
	StartReplacingFailedRun
)

func (k StartReasonKind) String() string {
	switch k {
	case StartRequiredForQuorum:
		return "required for quorum"
	case StartSlackRedundancy:
		return "slack redundancy"
	case StartDerivedValue:
		return "derived value"
	case StartRequeuedAfterStop:
		return "requeued after stop"
	case StartReplacingFailedRun:
		return "replacing failed run"
	}
	return "unknown"
}

// StartReason accompanies every start. Fields are valid per Kind: Value and
// Width only for StartDerivedValue, Attempt only for the two retry kinds.
// Pressure is always set.
//
// The two surplus kinds are worth keeping apart in the record. Redundancy
// started because a slot was going spare says nothing about the work; one
// started because its estimated value beat an incumbent's is the model
// actually deciding something, and telling them apart afterwards is the only
// way to know which of the two was doing the work.
type StartReason struct {
	Kind     StartReasonKind
	Value    float64 // estimated value that won the slot
	Width    float64 // how far its borrowed tangent was carried
	Pressure float64
	Attempt  int
}

func (r StartReason) String() string {
	s := r.Kind.String()
	switch r.Kind {
	case StartDerivedValue:
		s += fmt.Sprintf(" (value %.3f over width %.2f)", r.Value, r.Width)
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

// StopReason accompanies every stop. Value and BestSiblingValue are a snapshot
// of the ranking that produced the decision and are valid only for
// StopOutrankedBySibling; nothing else preserves them, since the ranking has
// usually moved on by the time anyone asks.
type StopReason struct {
	Kind             StopReasonKind
	Value            float64
	BestSiblingValue float64
	Pressure         float64
}

func (r StopReason) String() string {
	s := r.Kind.String()
	if r.Kind == StopOutrankedBySibling {
		s += fmt.Sprintf(" (value %.3f vs best %.3f)", r.Value, r.BestSiblingValue)
	}
	return fmt.Sprintf("%s at pressure %.2f", s, r.Pressure)
}
