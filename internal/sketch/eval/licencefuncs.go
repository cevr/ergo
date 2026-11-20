package eval

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The licence run of a law over function parameters (FUSION-EVIDENCE;
// owner q234; types.md, Laws 14, Over function parameters):
//
//  1. The law runs at int: each parameter's type is read with the law's
//     own type parameters at int (lawParamType), and its classes and
//     drawn values are ints, inside every type that names one (a
//     Maybe[T]'s field, a Tree[T]'s nodes: interp.atInt). No frame needs
//     the binding: a rewrite's sides are calls over its parameters
//     (S108), and a callee's type arguments come from its call.
//  2. A parameter of a function type takes a family of pure functions
//     (funcFamily) as its classes: each member is a class the run must
//     reach, alone and paired with each class of each other parameter, and
//     a drawn case takes a member at random. The family has a function of
//     each kind a fused loop could tell apart from the staged one: the
//     identity, a shift, a scale, a constant, and one that raises a defect
//     on some arguments; for a predicate, a threshold, a parity, the two
//     constants and one that raises a defect.
//  3. The right side runs where the build places it, under v1.FuseOr: a
//     defect of the right side runs the left side again, so the outcome
//     the run compares is the left side's (fusedOutcome). A defect the
//     left side raises and the right side does not is a refutation: the
//     fused form would return a value where the staged form defects.
//
// Prior art: QuickCheck generates functions (Test.QuickCheck.Function,
// shrinkable tables); Hypothesis's st.functions draws from a strategy per
// call. A fixed family is what a fusion law needs: the laws quantify over
// pure functions, and their failures differ by kind (which elements a
// stage visits, in which order, and which defect comes first), not by a
// function's values.

// lawParamType is t with each of law's own type parameters read at int,
// through slices and function types (the parameter types a fusion law
// takes).
func lawParamType(law *check.Law, t check.Type) check.Type {
	switch u := t.(type) {
	case *check.TypeParam:
		for _, tp := range law.TParams {
			if tp == u {
				return check.Typ[check.Int]
			}
		}
	case *check.Slice:
		return &check.Slice{Elem: lawParamType(law, u.Elem)}
	}
	return t
}

// funcFamily is the family of pure functions a parameter of the function
// type t takes, nil when the run has none for t: one or two parameters of
// an integer type (read at int), and a result of that type or bool. A
// two-parameter family (a fold's step, func(acc A, x T) A; LICENCE-ADT)
// has members that tell its arguments apart (a*3 + b, a projection of
// each), so a law that swaps them is refuted.
func funcFamily(law *check.Law, t check.Type) []licenceClass {
	sig, ok := t.(*check.Signature)
	if !ok || len(sig.Params) < 1 || len(sig.Params) > 2 || len(sig.Results) != 1 {
		return nil
	}
	for _, p := range sig.Params {
		in := lawParamType(law, p.Type)
		if b, ok := underOf(in).(*check.Basic); !ok || b.Kind != check.Int {
			return nil
		}
	}
	two := len(sig.Params) == 2
	// fn is a member: f reads the last argument as x and, for two, the
	// first as a.
	fn := func(name string, f func(fr *frame, at syntax.Node, a, x int64) (Value, *abrupt)) licenceClass {
		v := &nativeFunc{name: name, call: func(in *interp, fr *frame, at syntax.Node, args []Value) (Value, *abrupt) {
			mustCharge(in, fr, 1)
			x := args[len(args)-1].(int64)
			a := x
			if len(args) == 2 {
				a = args[0].(int64)
			}
			return f(fr, at, a, x)
		}}
		return licenceClass{name: name, value: func(*interp, *frame) Value { return v }}
	}
	switch out := lawParamType(law, sig.Results[0]).(type) {
	case *check.Basic:
		switch out.Kind {
		case check.Int:
			if two {
				return []licenceClass{
					fn("func a + x", func(_ *frame, _ syntax.Node, a, x int64) (Value, *abrupt) { return boxInt(a + x), nil }),
					fn("func a * 3 + x", func(_ *frame, _ syntax.Node, a, x int64) (Value, *abrupt) { return boxInt(a*3 + x), nil }),
					fn("func a", func(_ *frame, _ syntax.Node, a, x int64) (Value, *abrupt) { return boxInt(a), nil }),
					fn("func x", func(_ *frame, _ syntax.Node, a, x int64) (Value, *abrupt) { return boxInt(x), nil }),
					fn("func 7", func(_ *frame, _ syntax.Node, a, x int64) (Value, *abrupt) { return boxInt(7), nil }),
					fn("func defect at odd x", func(fr *frame, at syntax.Node, a, x int64) (Value, *abrupt) {
						if x%2 != 0 {
							return nil, fr.defect(at, "odd %d", x)
						}
						return boxInt(a + x/2), nil
					}),
				}
			}
			return []licenceClass{
				fn("func x", func(_ *frame, _ syntax.Node, _, x int64) (Value, *abrupt) { return boxInt(x), nil }),
				fn("func x + 1", func(_ *frame, _ syntax.Node, _, x int64) (Value, *abrupt) { return boxInt(x + 1), nil }),
				fn("func x * 3", func(_ *frame, _ syntax.Node, _, x int64) (Value, *abrupt) { return boxInt(x * 3), nil }),
				fn("func 7", func(_ *frame, _ syntax.Node, _, x int64) (Value, *abrupt) { return boxInt(7), nil }),
				fn("func defect at odd x", func(fr *frame, at syntax.Node, _, x int64) (Value, *abrupt) {
					if x%2 != 0 {
						return nil, fr.defect(at, "odd %d", x)
					}
					return boxInt(x / 2), nil
				}),
			}
		case check.Bool:
			if two {
				return []licenceClass{
					fn("func a < x", func(_ *frame, _ syntax.Node, a, x int64) (Value, *abrupt) { return a < x, nil }),
					fn("func a == x", func(_ *frame, _ syntax.Node, a, x int64) (Value, *abrupt) { return a == x, nil }),
					fn("func true", func(_ *frame, _ syntax.Node, a, x int64) (Value, *abrupt) { return true, nil }),
					fn("func false", func(_ *frame, _ syntax.Node, a, x int64) (Value, *abrupt) { return false, nil }),
					fn("func defect at x < 0", func(fr *frame, at syntax.Node, a, x int64) (Value, *abrupt) {
						if x < 0 {
							return nil, fr.defect(at, "negative %d", x)
						}
						return a > x, nil
					}),
				}
			}
			return []licenceClass{
				fn("func x > 0", func(_ *frame, _ syntax.Node, _, x int64) (Value, *abrupt) { return x > 0, nil }),
				fn("func x is even", func(_ *frame, _ syntax.Node, _, x int64) (Value, *abrupt) { return x%2 == 0, nil }),
				fn("func true", func(_ *frame, _ syntax.Node, _, x int64) (Value, *abrupt) { return true, nil }),
				fn("func false", func(_ *frame, _ syntax.Node, _, x int64) (Value, *abrupt) { return false, nil }),
				fn("func defect at x < 0", func(fr *frame, at syntax.Node, _, x int64) (Value, *abrupt) {
					if x < 0 {
						return nil, fr.defect(at, "negative %d", x)
					}
					return x > 2, nil
				}),
			}
		}
	}
	return nil
}

// fusedOutcome is what a site placed under v1.FuseOr gives, from its two
// sides' outcomes: the right side's, or the left side's where the right
// side raised a defect (FuseOr runs the left side again).
func fusedOutcome(left, right string) string {
	if isDefectOutcome(right) {
		return left
	}
	return right
}

// isDefectOutcome reports an outcome of sideOutcome that is a defect.
func isDefectOutcome(o string) bool {
	const p = "the defect "
	return len(o) >= len(p) && o[:len(p)] == p
}
