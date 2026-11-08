package unionreprbench

import "slices"

// Question 8: the clone at a task boundary (README Gap 45). A value that
// crosses a task boundary shares no mutable storage, so the clone copies
// every reachable slice, per static type.
//
//	type Held = Bag{Xs []int} | Tag{N int}
//
// Form A needs a type switch to find the slice, and rebuilding a Bag boxes
// it again. Form D clones the slice slot in place: no switch on the
// variant, and no box.

type Bag struct{ Xs []int }
type Tag struct{ N int }

type HeldA interface{ isHeldA() }

func (Bag) isHeldA() {}
func (Tag) isHeldA() {}

// HeldD: Bag.Xs has a []int slot, Tag.N an int slot.
type HeldD struct {
	tag uint8
	xs  []int
	n   int
}

//go:noinline
func cloneA(u HeldA) HeldA {
	switch v := u.(type) {
	case Bag:
		return Bag{Xs: slices.Clone(v.Xs)}
	case Tag:
		return v
	}
	panic("ergo: defect: no case of the match matched")
}

// cloneD clones each slot that holds a slice. An inactive slot is nil (the
// zero-slot invariant), so it costs one compare.
//
//go:noinline
func cloneD(u HeldD) HeldD {
	if u.xs != nil {
		u.xs = slices.Clone(u.xs)
	}
	return u
}

func heldBag(n int) (HeldA, HeldD) {
	xs := make([]int, n)
	for i := range xs {
		xs[i] = 256 + i
	}
	return Bag{Xs: xs}, HeldD{tag: 1, xs: xs}
}

func (u HeldD) wellFormed() bool {
	switch u.tag {
	case 1:
		return u.n == 0
	case 2:
		return u.xs == nil
	}
	return false
}
