package policy

import "errors"

var (
	ErrNoWorkload  = errors.New("policy: leaf has no workload")
	ErrNoChildren  = errors.New("policy: select has no children")
	ErrBadK        = errors.New("policy: k out of range")
	ErrUnknownKind = errors.New("policy: unknown group kind")
)

// Group is one node of a submitted tree: either a leaf holding a single
// Workload or a k-of-n Select over children. The interface is sealed, which
// makes "a node without children is a leaf" a property of the type rather
// than a convention a constructor has to keep.
type Group interface {
	isGroup()
	label() string
}

type leaf struct {
	w   Workload
	lbl string
}

func (leaf) isGroup()        {}
func (l leaf) label() string { return l.lbl }

type selectBestK struct {
	k        int
	children []Group
	lbl      string
}

func (selectBestK) isGroup()        {}
func (s selectBestK) label() string { return s.lbl }

// Leaf wraps one workload as a tree leaf. The workload must be idempotent: it
// may be stopped to free capacity and later rerun from scratch.
func Leaf(w Workload) (Group, error) {
	if w == nil {
		return nil, ErrNoWorkload
	}
	return leaf{w: w}, nil
}

// Select builds a node that is satisfied once k of its children are. Children
// may themselves be Selects, which is how a racing duplicate is expressed:
//
//	Select(1, w1, ..., w15)                      // keep the best of 15
//	Select(6, w1, ..., w9)                       // any 6 of 9 suffice
//	Select(n, l1, l2, Select(1, l3, l3dup), l4)  // l3 may be raced
func Select(k int, children ...Group) (Group, error) {
	if len(children) == 0 {
		return nil, ErrNoChildren
	}
	if k < 1 || k > len(children) {
		return nil, ErrBadK
	}
	for _, c := range children {
		if c == nil {
			return nil, ErrUnknownKind
		}
	}
	cs := make([]Group, len(children))
	copy(cs, children)
	return selectBestK{k: k, children: cs}, nil
}

// WithLabel returns g named for log lines and status reporting. The name has
// no effect on any decision.
func WithLabel(g Group, s string) Group {
	switch v := g.(type) {
	case leaf:
		v.lbl = s
		return v
	case selectBestK:
		v.lbl = s
		return v
	}
	return g
}
